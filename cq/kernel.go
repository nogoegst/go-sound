package cq

import (
	"fmt"
	"math"
	"math/cmplx"

	"github.com/mjibson/go-dsp/fft"
)

const DEBUG = false

type Properties struct {
	sampleRate float64
	// NOTE: minFrequency of the kernel is *not* min frequency of the CQ.
	// It is the frequency of the lowest note in the top octave.
	minFrequency  float64
	octaves       int
	binsPerOctave int
	fftSize       int
	fftHop        int
	atomsPerFrame int
	atomSpacing   int
	firstCentre   int
	lastCentre    int
	Q             float64
}

type Kernel struct {
	origin []int
	data   [][]complex128
}

type CQKernel struct {
	Properties Properties
	kernel     *Kernel
}

// TODO - clean up a lot.
func NewCQKernel(params CQParams) *CQKernel {
	// Constructor
	p := Properties{}
	p.sampleRate = params.sampleRate
	p.octaves = params.Octaves
	p.binsPerOctave = params.BinsPerOctave
	p.minFrequency = params.minFrequency * math.Pow(2.0, float64(p.octaves)-1.0+1.0/float64(p.binsPerOctave))

	// GenerateKernel
	q := params.q
	atomHopFactor := params.atomHopFactor
	thresh := params.threshold
	bpo := params.BinsPerOctave

	p.Q = q / (math.Pow(2, 1.0/float64(bpo)) - 1.0)

	maxNK := float64(int(math.Floor(p.Q*p.sampleRate/p.minFrequency + 0.5)))
	minNK := float64(int(math.Floor(p.Q*p.sampleRate/
		(p.minFrequency*math.Pow(2.0, (float64(bpo)-1.0)/float64(bpo))) + 0.5)))

	if minNK == 0 || maxNK == 0 {
		panic("Kernal minNK or maxNK is 0, can't make kernel")
	}

	p.atomSpacing = round(minNK*atomHopFactor + 0.5)
	p.firstCentre = p.atomSpacing * roundUp(math.Ceil(maxNK/2.0)/float64(p.atomSpacing))
	p.fftSize = nextPowerOf2(p.firstCentre + roundUp(maxNK/2.0))
	p.atomsPerFrame = roundDown(1.0 + (float64(p.fftSize)-math.Ceil(maxNK/2.0)-float64(p.firstCentre))/float64(p.atomSpacing))

	if DEBUG {
		fmt.Printf("atomsPerFrame = %v (q = %v, Q = %v, atomHopFactor = %v, atomSpacing = %v, fftSize = %v, maxNK = %v, firstCentre = %v)\n",
			p.atomsPerFrame, q, p.Q, atomHopFactor, p.atomSpacing, p.fftSize, maxNK, p.firstCentre)
	}

	p.lastCentre = p.firstCentre + (p.atomsPerFrame-1)*p.atomSpacing
	p.fftHop = (p.lastCentre + p.atomSpacing) - p.firstCentre

	if DEBUG {
		fmt.Printf("fftHop = %v\n", p.fftHop)
	}

	dataSize := p.binsPerOctave * p.atomsPerFrame

	kernel := Kernel{
		make([]int, 0, dataSize),
		make([][]complex128, 0, dataSize),
	}

	for k := 1; k <= p.binsPerOctave; k++ {
		nk := round(p.Q * p.sampleRate / (p.minFrequency * math.Pow(2, ((float64(k)-1.0)/float64(bpo)))))
		win := makeWindow(params.window, nk)
		fk := float64(p.minFrequency * math.Pow(2, ((float64(k)-1.0)/float64(bpo))))

		cmplxs := make([]complex128, nk, nk)
		for i := 0; i < nk; i++ {
			arg := (2.0 * math.Pi * fk * float64(i)) / p.sampleRate
			cmplxs[i] = cmplx.Rect(win[i], arg)
		}

		atomOffset := p.firstCentre - roundUp(float64(nk)/2.0)

		for i := 0; i < p.atomsPerFrame; i++ {
			shift := atomOffset + (i * p.atomSpacing)
			cin := make([]complex128, p.fftSize, p.fftSize)
			for j := 0; j < nk; j++ {
				cin[j+shift] = cmplxs[j]
			}

			cout := fft.FFT(cin)

			// Keep this dense for the moment (until after normalisation calculations)
			for j := 0; j < p.fftSize; j++ {
				if cmplx.Abs(cout[j]) < thresh {
					cout[j] = complex(0, 0)
				} else {
					cout[j] = complexTimes(cout[j], 1.0/float64(p.fftSize))
				}
			}

			kernel.origin = append(kernel.origin, 0)
			kernel.data = append(kernel.data, cout)
		}
	}

	if DEBUG {
		fmt.Printf("size = %v * %v (fft size = %v)\n", len(kernel.data), len(kernel.data[0]), p.fftSize)
	}

	// finalizeKernel

	// calculate weight for normalisation
	wx1 := maxidx(kernel.data[0])
	wx2 := maxidx(kernel.data[len(kernel.data)-1])

	subset := make([][]complex128, len(kernel.data), len(kernel.data))
	for i := 0; i < len(kernel.data); i++ {
		subset[i] = make([]complex128, 0, wx2-wx1+1)
	}
	for j := wx1; j <= wx2; j++ {
		for i := 0; i < len(kernel.data); i++ {
			subset[i] = append(subset[i], kernel.data[i][j])
		}
	}

	// Massive hack - precalculate above instead :(
	nrows, ncols := len(subset), len(subset[0])

	square := make([][]complex128, ncols, ncols) // conjugate transpose of subset * subset
	for i := 0; i < ncols; i++ {
		square[i] = make([]complex128, ncols, ncols)
	}

	for j := 0; j < ncols; j++ {
		for i := 0; i < ncols; i++ {
			v := complex(0, 0)
			for k := 0; k < nrows; k++ {
				v += subset[k][i] * cmplx.Conj(subset[k][j])
			}
			square[i][j] = v
		}
	}

	wK := []float64{}
	for i := int(1.0/q + 0.5); i < ncols-int(1.0/q+0.5)-2; i++ {
		wK = append(wK, cmplx.Abs(square[i][i]))
	}

	weight := float64(p.fftHop) / float64(p.fftSize)
	if len(wK) > 0 {
		weight /= mean(wK)
	}
	weight = math.Sqrt(weight)

	if DEBUG {
		fmt.Printf("weight = %v (from %v elements in wK, ncols = %v, q = %v)\n",
			weight, len(wK), ncols, q)
	}

	// apply normalisation weight, make sparse, and store conjugate
	// (we use the adjoint or conjugate transpose of the kernel matrix
	// for the forward transform, the plain kernel for the inverse
	// which we expect to be less common)

	sk := Kernel{
		make([]int, len(kernel.data), len(kernel.data)),
		make([][]complex128, len(kernel.data), len(kernel.data)),
	}
	for i := 0; i < len(kernel.data); i++ {
		sk.origin[i] = 0
		sk.data[i] = []complex128{}

		lastNZ := 0
		for j := len(kernel.data[i]) - 1; j >= 0; j-- {
			if cmplx.Abs(kernel.data[i][j]) != 0 {
				lastNZ = j
				break
			}
		}

		haveNZ := false
		for j := 0; j <= lastNZ; j++ {
			if haveNZ || cmplx.Abs(kernel.data[i][j]) != 0 {
				if !haveNZ {
					sk.origin[i] = j
				}
				haveNZ = true
				sk.data[i] = append(sk.data[i],
					complexTimes(cmplx.Conj(kernel.data[i][j]), weight))
			}
		}
	}

	return &CQKernel{p, &sk}
}

func (k *CQKernel) processForward(cv []complex128) []complex128 {
	// straightforward matrix multiply (taking into account m_kernel's
	// slightly-sparse representation)

	if len(k.kernel.data) == 0 {
		panic("Whoops - return empty array? is this even possible?")
	}

	nrows := k.Properties.binsPerOctave * k.Properties.atomsPerFrame

	rv := make([]complex128, nrows, nrows)
	for i := 0; i < nrows; i++ {
		// rv[i] = complex(0, 0)
		for j := 0; j < len(k.kernel.data[i]); j++ {
			rv[i] += cv[j+k.kernel.origin[i]] * k.kernel.data[i][j]
		}
	}
	return rv
}

func (k *CQKernel) ProcessInverse(cv []complex128) []complex128 {
	// matrix multiply by conjugate transpose of m_kernel. This is
	// actually the original kernel as calculated, we just stored the
	// conjugate-transpose of the kernel because we expect to be doing
	// more forward transforms than inverse ones.
	if len(k.kernel.data) == 0 {
		panic("Whoops - return empty array? is this even possible?")
	}

	ncols := k.Properties.binsPerOctave * k.Properties.atomsPerFrame
	nrows := k.Properties.fftSize

	rv := make([]complex128, nrows, nrows)
	for j := 0; j < ncols; j++ {
		i0 := k.kernel.origin[j]
		i1 := i0 + len(k.kernel.data[j])
		for i := i0; i < i1; i++ {
			rv[i] += cv[j] * cmplx.Conj(k.kernel.data[j][i-i0])
		}
	}
	return rv
}

func (k *CQKernel) BinCount() int {
	return k.Properties.octaves * k.Properties.binsPerOctave
}

func makeWindow(window Window, len int) []float64 {
	if len <= 0 {
		panic("Window too small!")
	}

	if window != SqrtBlackmanHarris {
		// HACK - support more?
		panic("Only SqrtBlackmanHarris window supported currently")
	}

	win := make([]float64, len-1, len-1)
	for i := 0; i < len-1; i++ {
		win[i] = 1.0
	}

	// Blackman Harris

	n := float64(len - 1)
	for i := 0; i < len-1; i++ {
		win[i] = win[i] * (0.35875 -
			0.48829*math.Cos(2.0*math.Pi*float64(i)/n) +
			0.14128*math.Cos(4.0*math.Pi*float64(i)/n) -
			0.01168*math.Cos(6.0*math.Pi*float64(i)/n))
	}

	win = append(win, win[0])

	switch window {
	case SqrtBlackmanHarris:
		fallthrough
	case SqrtBlackman:
		fallthrough
	case SqrtHann:
		for i, v := range win {
			win[i] = math.Sqrt(v) / float64(len)
		}

	case BlackmanHarris:
		fallthrough
	case Blackman:
		fallthrough
	case Hann:
		for i, v := range win {
			win[i] = v / float64(len)
		}
	}

	return win
}
