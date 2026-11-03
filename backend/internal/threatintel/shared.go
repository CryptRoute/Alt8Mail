package threatintel

import "strings"

// sharedDestinations are hosts that commonly carry links belonging to many
// different senders. Blocking one because a single reported message used it
// would create broad collateral damage, so they are excluded from automatic
// URL-host publication. A reviewer can still approve them explicitly.
//
// Matching is exact-host or dot-suffix: "sendgrid.net" covers
// "u123.sendgrid.net" but not "notsendgrid.net".
var sharedDestinations = []string{
	// URL shorteners.
	"bit.ly", "bitly.com", "tinyurl.com", "t.co", "goo.gl", "ow.ly", "buff.ly",
	"is.gd", "v.gd", "cutt.ly", "rebrand.ly", "shorturl.at", "rb.gy", "s.id",
	"tiny.cc", "lnkd.in", "t.ly", "short.io", "urlz.fr", "bit.do", "mcaf.ee",
	"x.gd", "qr.ae", "amzn.to", "youtu.be", "fb.me", "wp.me", "dlvr.it",
	"ift.tt", "tmblr.co", "snip.ly", "soo.gd", "clck.ru", "u.to", "shorte.st",
	"adf.ly", "ouo.io", "bc.vc", "sh.st",

	// Cloud storage and file sharing.
	"drive.google.com", "docs.google.com", "sheets.google.com", "slides.google.com",
	"forms.gle", "dropbox.com", "dl.dropboxusercontent.com", "onedrive.live.com",
	"1drv.ms", "box.com", "app.box.com", "mega.nz", "mega.io", "mediafire.com",
	"wetransfer.com", "we.tl", "files.fm", "icedrive.net", "pcloud.com",
	"sync.com", "sendspace.com", "gofile.io", "file.io", "anonfiles.com",

	// Content delivery and object storage.
	"amazonaws.com", "cloudfront.net", "azureedge.net", "blob.core.windows.net",
	"storage.googleapis.com", "storage.cloud.google.com", "googleusercontent.com",
	"gstatic.com", "akamaihd.net", "akamaized.net", "fastly.net", "jsdelivr.net",
	"unpkg.com", "cdnjs.cloudflare.com", "digitaloceanspaces.com",
	"backblazeb2.com", "r2.cloudflarestorage.com", "wasabisys.com", "b-cdn.net",
	"imgix.net", "cloudinary.com", "filestackcontent.com", "cloudapp.net",

	// Email service providers and click redirectors.
	"sendgrid.net", "list-manage.com", "mailchimp.com", "mcsv.net", "rsgsv.net",
	"mandrillapp.com", "mailgun.org", "mailgun.net", "constantcontact.com",
	"exacttarget.com", "hubspotlinks.com", "hubspotemail.net", "cmail19.com",
	"cmail20.com", "createsend.com", "campaign-archive.com", "klaviyo.com",
	"awstrack.me", "sendibt2.com", "sendibm1.com", "rs6.net", "mailchi.mp",
	"e2ma.net", "emailcampaigns.com", "verticalresponse.com",
}

// IsSharedDestination reports whether host belongs to the shared-destination
// guardrail catalog. The caller must pass a normalized hostname; the function
// also tolerates a trailing dot and mixed case for defensive use.
func IsSharedDestination(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	for _, destination := range sharedDestinations {
		if host == destination || strings.HasSuffix(host, "."+destination) {
			return true
		}
	}
	return false
}
