// Package mailcrypto is the protocol-neutral foundation for encrypted and
// signed mail in Alt8Mail.
//
// Phase 0 deliberately does not decrypt, encrypt, or persist anything. It
// defines the shared vocabulary used by both OpenPGP and S/MIME providers,
// detects protected MIME structures without rewriting the raw message, and
// keeps signed content byte-for-byte so later verification can use the exact
// bytes the sender signed.
package mailcrypto
