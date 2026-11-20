#!/usr/bin/env bash
set -Eeuo pipefail

alt8mail_install_go() {
  local arch go_version go_json go_sha archive
  arch="$(uname -m)"
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) ui_err "Unsupported architecture: $arch"; return 1 ;;
  esac
  if command -v go >/dev/null 2>&1 && go version | awk '{print $3}' \
      | grep -Eq '^go1\.(2[6-9]|[3-9][0-9])'; then
    ui_info "Using existing $(go version)"
    return 0
  fi
  go_json="$(curl -fsSL 'https://go.dev/dl/?mode=json')" || return 1
  go_version="$(printf '%s' "$go_json" | jq -r '[.[] | select(.stable == true)][0].version')"
  [[ -n "$go_version" && "$go_version" != "null" ]] \
    || { ui_err "Could not determine the current Go release"; return 1; }
  go_sha="$(printf '%s' "$go_json" | jq -r --arg v "$go_version" --arg a "$arch" '
    .[] | select(.version == $v) | .files[] |
    select(.os == "linux" and .arch == $a and .kind == "archive") | .sha256' | awk 'NR==1')"
  [[ -n "$go_sha" && "$go_sha" != "null" ]] \
    || { ui_err "Could not find a Go SHA-256 for $go_version/$arch"; return 1; }
  archive="/tmp/${go_version}.linux-${arch}.tar.gz"
  if ! curl -fsSL -o "$archive" "https://go.dev/dl/${go_version}.linux-${arch}.tar.gz"; then
    ui_warn "Official Go download unavailable; trying the Aliyun mirror"
    curl -fsSL -o "$archive" "https://mirrors.aliyun.com/golang/${go_version}.linux-${arch}.tar.gz" || return 1
  fi
  printf '%s  %s\n' "$go_sha" "$archive" | sha256sum -c - >/dev/null || return 1
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$archive" || return 1
  ln -sfn /usr/local/go/bin/go /usr/local/bin/go
  rm -f "$archive"
  ui_ok "Go ${go_version} installed"
}

phase_build() {
  local src_dir="$A8M_SRC_DIR"
  local build_root="${A8M_BUILD_ROOT:-/tmp/a8m-build}"

  if [[ -x "$src_dir/bin/a8m-server" ]]; then
    A8M_RELEASE_SOURCE="$src_dir"
    export A8M_RELEASE_SOURCE
    ui_ok "Using the prebuilt release in $src_dir"
    return 0
  fi

  [[ -x "$src_dir/scripts/build-release.sh" ]] \
    || { ui_err "scripts/build-release.sh is missing from $src_dir"; return 1; }
  alt8mail_install_go || return 1

  local go_arch=amd64
  [[ "$(uname -m)" == "aarch64" ]] && go_arch=arm64
  rm -rf "$build_root"
  install -d -m 0755 "$build_root" /var/cache/a8m/go-build
  export GOARCH_TARGET="$go_arch"
  export GOCACHE="${GOCACHE:-/var/cache/a8m/go-build}"
  export GOMAXPROCS="${GOMAXPROCS:-1}"
  export PATH="/usr/local/go/bin:$PATH"

  ui_task "Compiling Alt8Mail ${A8M_VERSION}; this can take several minutes"
  if ! run_stream build -- bash "$src_dir/scripts/build-release.sh" "$A8M_VERSION" "$build_root"; then
    ui_err "The release build failed. Last 30 log lines:"
    tail -n 30 "$A8M_LOG_DIR/build.log" >&2 || true
    return 1
  fi
  A8M_RELEASE_SOURCE="$build_root/alt8mail-$A8M_VERSION"
  [[ -x "$A8M_RELEASE_SOURCE/bin/a8m-server" ]] \
    || { ui_err "The release build did not produce bin/a8m-server"; return 1; }
  export A8M_RELEASE_SOURCE
  ui_ok "Release built: $A8M_RELEASE_SOURCE"
}
