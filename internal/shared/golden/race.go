//go:build race

package golden

// raceEnabled: the race suite skips some slow cases, so a Set's keys go
// unasked there for no fault of the file.
const raceEnabled = true
