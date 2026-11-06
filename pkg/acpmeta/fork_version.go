package acpmeta

import (
	"regexp"
	"strconv"
)

// forkVersionPattern 同时识别 CLI 的三段版本和 Cursor 日期版本。
var forkVersionPattern = regexp.MustCompile(`(?:^|\s|v)([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+\s]|$)`)

// VerifiedForkMode 只为不低于已核对上游协议的版本公布能力；未知版本保持关闭。
func VerifiedForkMode(version, minimum, mode string) string {
	actual, floor := forkVersionPattern.FindStringSubmatch(version), forkVersionPattern.FindStringSubmatch(minimum)
	if len(actual) != 4 || len(floor) != 4 {
		return ForkUnsupported
	}
	for i := 1; i < 4; i++ {
		a, _ := strconv.Atoi(actual[i])
		b, _ := strconv.Atoi(floor[i])
		if a > b {
			return mode
		}
		if a < b {
			return ForkUnsupported
		}
	}
	return mode
}

// VerifiedForkModeInMinor 仅在已核对的主次版本内开放依赖私有持久格式的分叉适配。
func VerifiedForkModeInMinor(version, minimum, mode string) string {
	actual, floor := forkVersionPattern.FindStringSubmatch(version), forkVersionPattern.FindStringSubmatch(minimum)
	if len(actual) != 4 || len(floor) != 4 {
		return ForkUnsupported
	}
	if actual[1] != floor[1] || actual[2] != floor[2] {
		return ForkUnsupported
	}
	return VerifiedForkMode(version, minimum, mode)
}
