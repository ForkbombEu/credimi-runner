package dashboard

import "strings"

type TargetProfile struct {
	Type           string
	Title          string
	PrimaryFields  []string
	AdvancedFields []string
}

func TargetProfiles() []TargetProfile {
	return []TargetProfile{
		{
			Type:          "android_phone",
			Title:         "Android phone",
			PrimaryFields: []string{"CREDIMI_RUNNER_DEVICE_MODE", "CREDIMI_RUNNER_SERIAL", "CREDIMI_RUNNER_WIFI_IP", "CREDIMI_RUNNER_WIFI_PORT"},
			AdvancedFields: []string{
				"RUNNER_IMAGE",
			},
		},
		{
			Type:          "android_emulator",
			Title:         "Android emulator",
			PrimaryFields: []string{"BASE_NAME"},
			AdvancedFields: []string{
				"RUNNER_IMAGE", "ANDROID_KEYS_DIR", "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH", "GOLDEN_PATH",
			},
		},
		{
			Type:          "ios_simulator",
			Title:         "iOS simulator",
			PrimaryFields: []string{"BASE_NAME"},
			AdvancedFields: []string{
				"RUNNER_IMAGE",
			},
		},
		{
			Type:          "redroid",
			Title:         "Redroid",
			PrimaryFields: []string{"REDROID_DATA_DIR", "REDROID_DATA_TAR"},
			AdvancedFields: []string{
				"RUNNER_IMAGE", "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD",
			},
		},
	}
}

func (d PageData) TargetProfiles() []TargetProfile {
	return TargetProfiles()
}

func (d PageData) ActiveTargetProfile() TargetProfile {
	runnerType := strings.TrimSpace(d.Runner.Get("CREDIMI_RUNNER_TYPE"))
	for _, profile := range TargetProfiles() {
		if profile.Type == runnerType {
			return profile
		}
	}
	return TargetProfiles()[0]
}
