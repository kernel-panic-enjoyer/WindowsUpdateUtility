package updater

import (
	"crypto/sha256"
	"encoding/hex"

	_ "embed"
)

//go:embed assets/app.ico
var appIconICO []byte

//go:embed assets/broker/WindowsUpdater.StoreInventoryBroker.exe
var embeddedStoreInventoryBroker []byte

//go:embed assets/ui.css
var uiCSS string

//go:embed assets/ui.js
var uiJS string

func appIconVersion() string {
	sum := sha256.Sum256(appIconICO)
	return hex.EncodeToString(sum[:])[:12]
}

func frontendAssetVersion() string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(uiCSS))
	_, _ = hash.Write([]byte(uiJS))
	return hex.EncodeToString(hash.Sum(nil))[:12]
}
