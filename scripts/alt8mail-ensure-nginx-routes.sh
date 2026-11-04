#!/usr/bin/env bash
# Ensure the public Alt8Mail routes are proxied by the active nginx site.
#
# Application updates do not overwrite an administrator's nginx site. This
# script adds a small include to the TLS server block only when a required
# proxy location is missing, validates the result with nginx -t and restores
# the previous file if validation fails.
set -Eeuo pipefail

SITE="${A8M_NGINX_SITE:-/etc/nginx/sites-available/a8m}"
SNIPPET="${A8M_NGINX_SNIPPET:-/etc/nginx/snippets/a8m-public-routes.conf}"
UPSTREAM="${A8M_NGINX_UPSTREAM:-127.0.0.1:8080}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[[ -f "$SITE" ]] || { log "nginx site $SITE not found; skipping public route check"; exit 0; }
command -v nginx >/dev/null 2>&1 || { log "nginx is not installed; skipping public route check"; exit 0; }

missing=()
for path in /files/s/ /upload/ /calendar/rsvp/ /calendar/poll/ /calendar/feed/; do
  if ! grep -Eq "location[[:space:]]+[^\{]*[[:space:]]${path//\//\\/}" "$SITE"; then
    missing+=("$path")
  fi
done
if [[ ${#missing[@]} -eq 0 ]]; then
  exit 0
fi

log "adding missing public nginx routes: ${missing[*]}"
install -d -m 0755 "$(dirname "$SNIPPET")"
{
  printf '# Managed by Alt8Mail. Public download, upload and calendar routes.\n'
  for path in "${missing[@]}"; do
    body=""
    [[ "$path" == "/upload/" ]] && body='        client_max_body_size 12m;\n'
    printf 'location ^~ %s {\n        proxy_pass http://%s;\n        proxy_http_version 1.1;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $remote_addr;\n        proxy_set_header X-Forwarded-Proto $scheme;\n%b    }\n' "$path" "$UPSTREAM" "$body"
  done
} > "$SNIPPET"
chmod 0644 "$SNIPPET"

backup="$(mktemp)"
updated="$(mktemp)"
trap 'rm -f "$backup" "$updated"' EXIT
cp -a "$SITE" "$backup"

if ! awk -v snippet="$SNIPPET" '
{
  line = $0
  if (!in_server && line ~ /^[[:space:]]*server[[:space:]]*\{/) {
    in_server = 1; depth = 0; tls = 0
  }
  if (in_server) {
    copy = line
    if (copy ~ /listen[^;]*443/) tls = 1
    opens = gsub(/\{/, "{", copy)
    closes = gsub(/\}/, "}", copy)
    if (closes > 0 && depth + opens - closes == 0) {
      if (tls && !inserted) {
        print "    include " snippet ";"
        inserted = 1
      }
      in_server = 0
    }
    depth += opens - closes
    print line
    next
  }
  print line
}
END { if (!inserted) exit 3 }
' "$backup" > "$updated"; then
  log "no TLS server block found in $SITE; leaving nginx unchanged"
  exit 0
fi
cat "$updated" > "$SITE"

if ! nginx -t >/dev/null 2>&1; then
  cp -a "$backup" "$SITE"
  nginx -t >/dev/null 2>&1 || true
  log "nginx rejected the public route include; the previous site file was restored"
  exit 1
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl reload nginx || systemctl restart nginx || true
else
  nginx -s reload || true
fi
log "public nginx routes installed and nginx reloaded"
