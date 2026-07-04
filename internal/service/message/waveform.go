package message

import (
	"math"
	"math/rand"
)

func generatePTTWaveform(seconds int) []byte {
	const size = 64
	waveform := make([]byte, size)

	numSegments := 2 + rand.Intn(3)
	if seconds > 10 {
		numSegments = 3 + rand.Intn(3)
	}

	type segment struct {
		center    float64
		width     float64
		intensity float64
	}

	segments := make([]segment, numSegments)
	for i := range segments {
		basePos := (float64(i) + 0.5) / float64(numSegments)
		segments[i] = segment{
			center:    basePos*float64(size) + float64(rand.Intn(6)-3),
			width:     float64(4 + rand.Intn(10)),
			intensity: 30.0 + float64(rand.Intn(70)),
		}
	}

	mainPeak := rand.Intn(numSegments)
	segments[mainPeak].intensity = 70.0 + float64(rand.Intn(50))
	segments[mainPeak].width = float64(6 + rand.Intn(12))

	for i := 0; i < size; i++ {
		val := float64(rand.Intn(8))

		for _, seg := range segments {
			dist := math.Abs(float64(i) - seg.center)
			if dist < seg.width {
				normalized := dist / seg.width
				contribution := seg.intensity * math.Exp(-3.0*normalized*normalized)
				contribution *= 0.7 + 0.3*math.Sin(float64(i)*0.8+float64(rand.Intn(10)))
				val += contribution
			}
		}

		if val > 10 {
			val += float64(rand.Intn(int(val/5) + 1))
		}

		if val > 255 {
			val = 255
		}
		waveform[i] = byte(val)
	}

	for i := 0; i < 3; i++ {
		factor := float64(i+1) / 4.0
		waveform[i] = byte(float64(waveform[i]) * factor)
		waveform[size-1-i] = byte(float64(waveform[size-1-i]) * factor)
	}

	return waveform
}
