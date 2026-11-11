package media

import "fmt"

// Preset defines negotiated video resolution, framerate and H.264 profile level.
type Preset int

const (
	Preset720p30 Preset = iota
	Preset1080p30
)

func (p Preset) String() string {
	switch p {
	case Preset1080p30:
		return "均衡 1080p30"
	default:
		return "流畅 720p30"
	}
}

func (p Preset) ConfigString() string {
	switch p {
	case Preset1080p30:
		return "1080p30"
	default:
		return "720p30"
	}
}

func (p Preset) MaxDimensions() (width, height, fps, level int) {
	switch p {
	case Preset1080p30:
		return 1920, 1080, 30, 40
	default:
		return 1280, 720, 30, 31
	}
}

func (p Preset) ProfileLevelID() string {
	switch p {
	case Preset1080p30:
		return "42e028"
	default:
		return "42e01f"
	}
}

func ParsePreset(name string) Preset {
	if name == "1080p" || name == "1080p30" || name == "均衡 1080p30" {
		return Preset1080p30
	}
	return Preset720p30
}

func ParseCodec(name string) Codec {
	switch name {
	case "hardware", "hardware_required", "要求硬件", "仅硬件编码":
		return HardwareRequired
	case "software", "软件", "软件编码":
		return Software
	default:
		return Auto
	}
}

func (c Codec) ConfigString() string {
	switch c {
	case HardwareRequired:
		return "hardware"
	case Software:
		return "software"
	default:
		return "auto"
	}
}

func ProfileLevelForLevel(level int) string {
	switch level {
	case 40:
		return "42e028"
	case 42:
		return "42e02a"
	default:
		return "42e01f"
	}
}

func LevelForProfileLevelID(id string) int {
	switch id {
	case "42e028":
		return 40
	case "42e02a":
		return 42
	default:
		return 31
	}
}

func FormatResolution(w, h int) string {
	if w <= 0 || h <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d×%d", w, h)
}
