// mersenne - mersenne testing
//
// FIXME this code was converted from the ARM prime project and needs a
// lot of tidying up - getting rid of global variables etc.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"strconv"
)

// Globals
var (
	// Flags
	cpuprofile = flag.String("cpuprofile", "", "Write cpu profile to file")
	iterations = flag.Uint64("iterations", 0, "Number of iterations to check run - 0 for full test")
)

// Data for mersenne primality checking
type Mersenne struct {
	log_n          uint8
	n              uint
	x              []uint64
	digit_weight   []uint64
	digit_unweight []uint64

	exponent          uint64
	root2             uint64
	digit_width0      uint8  // bits in a small digit
	digit_width1      uint8  // bits in a large digit
	digit_width_0_max uint32 // max size of a small digit
	digit_widths      []uint8

	fft Fft
}

// Make a new Mersenne prime checker
//
// Call Init or AutoInit before using
func NewMersenne() *Mersenne {
	return new(Mersenne)
}

// Try to do dwt...
//
// n is size of transform
// p is the exponent we want to test
// i is the number of the element
//
//     if (2*(pMersenne/FFTLEN) + LG2_FFTLEN >= 62*NPFFT) {
//         fprintf(stderr, "FFTLEN = %s insufficient for pMersenne = %s\n",
//                 uint64out(FFTLEN), uint64out(pMersenne));
//         exit(1);
//     }
//
// return false for failed true for ok
func (m *Mersenne) Initialise(log_n uint8, exponent uint64) bool {
	m.exponent = exponent
	m.log_n = log_n
	m.n = uint(1) << log_n
	width := exponent / uint64(m.n)

	// Make sure the FFT is long enough so that each 'digit' can't
	// overflow a 63 bit number (mod p is slightly less that 64
	// bits) after the convolution
	// Some digits are (w+1) wide so use this for safety
	// (w+1)*2+log_n >= 63
	if 2*width+uint64(log_n) >= 61 {
		return false
	}

	// calculate the n-th root of two
	// An n-th root of two can be generated by 7^(5*(p-1)/192/n) mod p
	m.root2 = mod_pow(7, (MOD_P-1)/192/uint64(m.n)*5)
	if mod_pow(m.root2, uint64(m.n)) != 2 {
		panic("Root of 2 is wrong")
	}

	m.digit_width0 = uint8(width)
	m.digit_width_0_max = uint32(1) << width
	m.digit_width1 = uint8(width) + 1

	// memory allocation
	m.digit_weight = make([]uint64, m.n)
	m.digit_unweight = make([]uint64, m.n)
	m.digit_widths = make([]uint8, m.n)
	m.x = make([]uint64, m.n)

	// digit weights
	m.digit_weight[0] = 1
	m.digit_unweight[0] = mod_inv(uint64(m.n))
	old_addr := uint64(0)
	for i := uint(0); i <= m.n; i++ {
		t := uint64(m.exponent) * uint64(i)
		r := t % uint64(m.n)
		addr := t / uint64(m.n)
		if r>>32 != 0 {
			return false
		}
		if uint32(r) != 0 { // do ceil
			addr++
		}
		if addr>>32 != 0 {
			return false
		}

		// bit position for digit[i] is ceil((exponent * i) / n)
		if i > 0 {
			digit_width := addr - old_addr
			m.digit_widths[i-1] = uint8(digit_width)
			if digit_width != width && digit_width != width+1 {
				return false
			}
			// printf("digit_widths[%i] = %i from %i to %i\n", i-1, digit_widths[i-1], o, a-1);

			// dwt weight is 2^(1 - ((exponent * i mod n)/n))
			if i < m.n {
				r = uint64(m.n) - r
				m.digit_weight[i] = mod_pow(m.root2, r)
				m.digit_unweight[i] = mod_inv(mod_mul(m.digit_weight[i], uint64(m.n)))
			}
		}

		old_addr = addr
	}

	// fft_initialise
	if m.log_n <= 10 {
		m.fft = NewFftUnrolled(m.log_n)
	} else if m.log_n%2 == 0 {
		m.fft = NewFftFourStep(m.log_n)
	} else {
		m.fft = NewFftFastish(m.log_n)
	}

	return true
}

// Calls Initialise with increasing sizes until we find a bit enough FFT size
//
// Returns m passed in for chaining
func (m *Mersenne) AutoInitialise(exponent uint64) *Mersenne {
	for log_n := uint8(0); log_n <= 26; log_n++ {
		if m.Initialise(log_n, exponent) {
			return m
		}
	}
	log.Fatal("Exponent too big")
	return nil
}

// Return the bottom 64 bits
// Assumes a carry propagated array where all digits are within their widths
// And that all digit widths are <= 32
//
// If the residue is 0 then it checks the whole array to double check
// that is zero for a proper primality check
func (m *Mersenne) Residue() uint64 {
	i := uint(0)
	j := uint(0)
	r := uint64(0)
	for ; i < 64 && j < m.n; i, j = i+uint(m.digit_widths[j]), j+1 {
		r |= m.x[j] << i
	}
	if r != 0 {
		return r
	}
	r = 0
	for j = 0; j < m.n; j++ {
		r |= m.x[j]
	}
	return r
}

// This adds an uint32 to x
// We assume that x < 2^minimum_digit_width
//
// It assumes that x has had the first round of carry propagation done on it
// already so each digit[i] is < 2^digit_widths[i] < 2^32
func (m *Mersenne) Add32(c uint32, i uint) {
	for c != 0 {
		for ; i < m.n; i++ {
			y := uint64(1) << m.digit_widths[i]
			m.x[i] += uint64(c)
			if m.x[i] >= y {
				m.x[i] -= y
				c = 1
			} else {
				return // done if no carry
			}
		}
		//         printf("Wrapping round the end in m.Add32\n");
		i = 0
	}
}

// This subtracts an uint32 from x
// We assume that x < 2^minimum_digit_width
//
// and that x has had the first round of carry propagation done on it
// already so each digit[i] is < 2^digit_widths[i] < 2^32
func (m *Mersenne) Sub32(c uint32) {
	for c != 0 {
		for i := uint(0); i < m.n; i++ {
			y := uint64(1) << m.digit_widths[i]
			m.x[i] -= uint64(c)
			if m.x[i] >= y {
				m.x[i] += y
				c = 1
			} else {
				return // done if no carry
			}
		}
		//         printf("Wrapping round the end in m.Sub32\n");
	}
}

// This adds an uint64 to x
//
// It assumes that x has had the first round of carry propagation done on it
// already so each digit[i] is < 2^digit_widths[i] < 2^32
func (m *Mersenne) Add64(c uint64) {
	for c != 0 {
		for i := uint(0); i < m.n; i++ {
			m.x[i] = mod_adc(m.x[i], m.digit_widths[i], &c)
			t := uint32(c)
			if (c>>32) != 0 && t < m.digit_width_0_max {
				if t != 0 {
					m.Add32(t, i+1) // carry in 32 bits if possible
				}
				return // finished if carry is 0
			}
		}
		// printf("Wrapping round the end in m.Add64\n");
	}
}

// This does one interation
func (m *Mersenne) Mul() {
	c := uint64(0)

	// weight the input
	mod_vector_mul(m.n, m.x, m.digit_weight)

	// transform
	m.fft.Fft(m.x)

	// point multiply
	mod_vector_sqr(m.n, m.x)

	// untransform
	m.fft.InvFft(m.x)

	// unweight and normalise the output
	mod_vector_mul(m.n, m.x, m.digit_unweight)

	// carry propagation
	for i := uint(0); i < m.n; i++ {
		// printf("x[%i]=0x%016llX, carry=0x%016llX\n", i, m.x[i], carry);
		m.x[i] = mod_adc(m.x[i], m.digit_widths[i], &c)
		// printf("x[%i]=0x%016llX, carry=0x%016llX\n", i, m.x[i], carry);
	}
	if c != 0 {
		// printf("Wrapping carry in m.Mul carry propagation\n");
		m.Add64(c)
	}

	// subtract 2
	m.Sub32(2)
}

// Sets the mersenne array up and runs it for the number of iterations asked for
func (m *Mersenne) Run(iterations uint64) {
	if iterations == 0 {
		iterations = m.exponent - 2
	}
	m.x[0] = 4
	for i := uint64(0); i < iterations; i++ {
		m.Mul()
	}
}

// syntaxError prints the syntax
func syntaxError() {
	fmt.Fprintf(os.Stderr, `Mersenne prime tester

Usage:

prog [options] q

where q = Mersenne exponent to test

Options:
`)
	flag.PrintDefaults()
}
func main() {
	flag.Usage = syntaxError
	flag.Parse()
	args := flag.Args()

	// Setup profiling if desired
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if len(args) != 1 {
		syntaxError()
		log.Fatal("No exponent supplied")
	}

	exponent, err := strconv.ParseUint(args[0], 0, 64)
	if err != nil {
		syntaxError()
		log.Fatalf("Couldn't parse exponent: %v\n", err)
	}

	m := NewMersenne().AutoInitialise(exponent)

	fmt.Printf("Testing 2**%d-1 with fft size 2**%d for %d iterations\n", m.exponent, m.log_n, *iterations)
	m.Run(*iterations)
	fmt.Printf("Residue 0x%016X\n", m.Residue())
}
