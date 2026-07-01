package updater

import (
	"crypto/sha256"
	"encoding/hex"

	_ "embed"
)

const assetVersionHexLength = 12

//go:embed assets/app.ico
var appIconICO []byte

//go:embed assets/ui.css
var uiCSS string

//go:embed assets/ui.js
var uiJS string

func appIconVersion() string {
	iconDigest := sha256.Sum256(appIconICO)
	return shortAssetVersion(iconDigest[:])
}

func frontendAssetVersion() string {
	assetDigest := sha256.New()
	_, _ = assetDigest.Write([]byte(uiCSS))
	_, _ = assetDigest.Write([]byte(uiJS))
	return shortAssetVersion(assetDigest.Sum(nil))
}

func shortAssetVersion(digest []byte) string {
	return hex.EncodeToString(digest)[:assetVersionHexLength]
}
