// Command mailcrypto-analyze is a Phase 0 interop helper. It prints the
// structural encryption/signature view of an RFC 822 message without
// decrypting it or changing any bytes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type partSummary struct {
	Index            int           `json:"index"`
	Depth            int           `json:"depth"`
	MediaType        string        `json:"mediaType"`
	Disposition      string        `json:"disposition,omitempty"`
	TransferEncoding string        `json:"transferEncoding,omitempty"`
	Size             int           `json:"size"`
	Children         []partSummary `json:"children,omitempty"`
}

func summarize(part *mailcrypto.Part) partSummary {
	if part == nil {
		return partSummary{}
	}
	summary := partSummary{
		Index:            part.Index,
		Depth:            part.Depth,
		MediaType:        part.MediaType,
		Disposition:      part.Disposition,
		TransferEncoding: part.TransferEncoding,
		Size:             len(part.Raw),
	}
	for _, child := range part.Children {
		summary.Children = append(summary.Children, summarize(child))
	}
	return summary
}

func main() {
	parts := flag.Bool("parts", false, "include a MIME part tree")
	pretty := flag.Bool("pretty", true, "indent JSON output")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: mailcrypto-analyze [-parts] [-pretty=false] <message.eml>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	file, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, mailcrypto.MaxAnalyzeBytes+1))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	analysis, err := mailcrypto.Analyze(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	output := struct {
		mailcrypto.Analysis
		PartTree *partSummary `json:"partTree,omitempty"`
	}{Analysis: analysis}
	if *parts && len(analysis.Parts) > 0 {
		summary := summarize(analysis.Parts[0])
		output.PartTree = &summary
	}
	encoder := json.NewEncoder(os.Stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
