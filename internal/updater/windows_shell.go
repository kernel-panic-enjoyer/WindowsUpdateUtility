package updater

import "os/exec"

func openURL(url string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = hiddenSysProcAttr()
	return cmd.Start()
}
