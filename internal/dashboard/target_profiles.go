package dashboard

import (
	"strings"

	dashboardruntime "github.com/forkbombeu/credimi-runner/internal/dashboard/runtime"
)

type TargetProfile struct {
	Type           string
	Title          string
	PrimaryFields  []string
	AdvancedFields []string
}

func TargetProfilesForGOOS(goos string) []TargetProfile {
	all := map[string]TargetProfile{
		"android_phone": {
			Type:          "android_phone",
			Title:         "Android phone",
			PrimaryFields: []string{"CREDIMI_RUNNER_DEVICE_MODE", "CREDIMI_RUNNER_SERIAL", "CREDIMI_RUNNER_WIFI_IP", "CREDIMI_RUNNER_WIFI_PORT"},
			AdvancedFields: []string{
				"RUNNER_IMAGE",
			},
		},
		"android_emulator": {
			Type:          "android_emulator",
			Title:         "Android emulator",
			PrimaryFields: []string{"BASE_NAME"},
			AdvancedFields: []string{
				"RUNNER_IMAGE", "ANDROID_KEYS_DIR", "HOST_AVD_HOME_PATH", "HOST_AVD_GOLDEN_PATH", "GOLDEN_PATH",
			},
		},
		"ios_simulator": {
			Type:          "ios_simulator",
			Title:         "iOS simulator",
			PrimaryFields: []string{"BASE_NAME"},
			AdvancedFields: []string{
				"RUNNER_IMAGE",
			},
		},
		"redroid": {
			Type:          "redroid",
			Title:         "Redroid",
			PrimaryFields: []string{"REDROID_DATA_DIR", "REDROID_DATA_TAR"},
			AdvancedFields: []string{
				"RUNNER_IMAGE", "AVDCTL_SSH_TARGET", "AVDCTL_SSH_PASSWORD", "AVDCTL_SSH_KNOWN_HOSTS_PATH", "AVDCTL_SUDO", "AVDCTL_SUDO_PASSWORD",
			},
		},
	}

	choices := dashboardruntime.RunnerTypeChoices(goos)
	profiles := make([]TargetProfile, 0, len(choices))
	for _, choice := range choices {
		profile, ok := all[choice]
		if ok {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

func TargetProfiles() []TargetProfile {
	return TargetProfilesForGOOS(currentGOOS())
}

func (d PageData) TargetProfiles() []TargetProfile {
	return TargetProfilesForGOOS(currentGOOS())
}

func (d PageData) ActiveTargetProfile() TargetProfile {
	runnerType := strings.TrimSpace(d.Runner.Get("CREDIMI_RUNNER_TYPE"))
	profiles := d.TargetProfiles()
	for _, profile := range profiles {
		if profile.Type == runnerType {
			return profile
		}
	}
	if len(profiles) == 0 {
		return TargetProfile{}
	}
	return profiles[0]
}
