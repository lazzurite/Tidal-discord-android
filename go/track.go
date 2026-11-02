package bridge

type Track struct {
	Player     string
	Title      string
	Artist     string
	Album      string
	Playing    bool
	PositionMs int64
	DurationMs int64
}