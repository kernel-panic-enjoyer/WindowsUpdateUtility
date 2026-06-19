package updater

import (
	"crypto/sha256"
	"encoding/hex"

	_ "embed"
)

//go:embed assets/app.ico
var appIconICO []byte

func appIconVersion() string {
	sum := sha256.Sum256(appIconICO)
	return hex.EncodeToString(sum[:])[:12]
}
