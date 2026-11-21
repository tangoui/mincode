package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mincode/app/backend/updateauth"
)

func main() {
	input := flag.String("input", "", "input manifest JSON path")
	output := flag.String("output", "", "output signed manifest JSON path")
	flag.Parse()
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "input and output paths are required")
		os.Exit(2)
	}
	privateKey := os.Getenv("MINCODE_UPDATE_MANIFEST_PRIVATE_KEY")
	if privateKey == "" {
		fmt.Fprintln(os.Stderr, "MINCODE_UPDATE_MANIFEST_PRIVATE_KEY is required")
		os.Exit(2)
	}
	body, err := os.ReadFile(*input)
	if err != nil {
		fatal(err)
	}
	var manifest updateauth.Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		fatal(fmt.Errorf("decode manifest: %w", err))
	}
	if err := updateauth.Sign(&manifest, privateKey); err != nil {
		fatal(err)
	}
	if publicKey := os.Getenv("MINCODE_UPDATE_MANIFEST_PUBLIC_KEY"); publicKey != "" {
		if err := updateauth.Verify(manifest, publicKey); err != nil {
			fatal(fmt.Errorf("签名密钥与客户端公钥不匹配: %w", err))
		}
	}
	signed, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	signed = append(signed, '\n')
	if err := os.WriteFile(*output, signed, 0o600); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
