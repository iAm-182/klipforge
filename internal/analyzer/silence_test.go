package analyzer

import "testing"

func TestTrimSilenceFromEnergy_TrimsDeadAirAndProtectsPeakPause(t *testing.T) {
	// 30-second clip:
	// [0..6] dead air (0.04) -> should trim lead-in (~5s trimmed, keeping 1s margin)
	// [7..15] setup speech (0.55)
	// [16..18] SILENT COMIC PAUSE right before peakSec=19! (0.02) -> MUST stay intact!
	// [19..23] punchline/laughter peak (0.95)
	// [24..29] trailing dead air (0.03) -> should trim tail
	energy := make([]float64, 30)
	for i := 0; i < 30; i++ {
		energy[i] = 0.03
	}
	for i := 7; i <= 15; i++ {
		energy[i] = 0.55
	}
	for i := 19; i <= 23; i++ {
		energy[i] = 0.95
	}

	ns, ne, trimmed, reason := TrimSilenceFromEnergy(energy, 0, 30, 19.0, DefaultSilenceTrimConfig())
	if !trimmed {
		t.Fatalf("expected dead air to be trimmed, got trimmed=false (%s)", reason)
	}
	if ns < 5.0 || ns > 7.0 {
		t.Fatalf("expected lead-in trimmed to ~6.0s (margin 1s before active=7), got %.2f", ns)
	}
	if ne < 24.0 || ne > 26.0 {
		t.Fatalf("expected tail trimmed to ~25.0s (margin 1s after active=23), got %.2f", ne)
	}

	// Edge case: entire clip before peak is quiet comic buildup -> NEVER trims inside peakSec-4s!
	quietBuildup := make([]float64, 25)
	for i := 0; i < 25; i++ {
		quietBuildup[i] = 0.02
	}
	quietBuildup[14] = 0.95
	ns2, ne2, _, _ := TrimSilenceFromEnergy(quietBuildup, 0, 25, 14.0, DefaultSilenceTrimConfig())
	if ns2 > 10.01 {
		t.Fatalf("peak comic pause violated! peakSec=14 with 4s protection must keep start <= 10.0, got %.2f", ns2)
	}
	if ne2 < 16.99 {
		t.Fatalf("peak tail protection violated! peakSec=14 with 3s protection must keep end >= 17.0, got %.2f", ne2)
	}
}
