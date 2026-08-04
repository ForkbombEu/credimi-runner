package dashboard

import dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"

const (
	defaultPhoneImage      = dashboardruntime.DefaultPhoneImage
	defaultEmulatorImage   = dashboardruntime.DefaultEmulatorImage
	defaultAndroidWiFiPort = dashboardruntime.DefaultWiFiPort
)

func WriteComposeFile(dir string, vals map[string]string) error {
	return dashboardruntime.WriteComposeFile(dir, dashboardruntime.Values(vals))
}
