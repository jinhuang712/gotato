//go:build race

package workload

// raceEnabled says the tests run under the race detector, which makes the
// 2,000-Turn long_session task take over a minute.
const raceEnabled = true
