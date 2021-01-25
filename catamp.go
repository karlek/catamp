// It really whips the cat's ass.
//
// Usage:
// $ catamp *.flac
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"time"
	"strings"
	"os"
	"sync"
	"os/exec"

	"github.com/mesilliac/pulse-simple"
	"github.com/mewkiz/flac"
	"github.com/sirupsen/logrus"
)

const (
	SeekSize = 44100*1
)

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
	}

	// Disable input buffering.
	exec.Command("stty", "-F", "/dev/tty", "cbreak", "min", "1").Run()
	// Do not display entered characters on the screen.
	exec.Command("stty", "-F", "/dev/tty", "-echo").Run()
	// Restore the echoing state when exiting.
	defer exec.Command("stty", "-F", "/dev/tty", "echo").Run()

	for _, f := range flag.Args() {
		if err := catamp(f); err != nil {
			log.Fatalln(err)
		}
	}
}

func sampleFormat(stream *flac.Stream) (sf pulse.SampleFormat, size int) {
	if stream.Info.BitsPerSample == 16 {
		sf = pulse.SAMPLE_S16BE
	} else if stream.Info.BitsPerSample == 24 {
		sf = pulse.SAMPLE_S24_32BE
	} else {
		panic(fmt.Sprintf("Not implemented: bits per sample %d", stream.Info.BitsPerSample))
	}
	return sf, int(sf.SampleSize())
}

func catamp(filename string) (err error) {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	stream, err := flac.NewSeek(f)
	if err != nil {
		return err
	}
	defer stream.Close()

	sf, size := sampleFormat(stream)
	ss := pulse.SampleSpec{
		Format:   sf,
		Rate:     stream.Info.SampleRate,
		Channels: stream.Info.NChannels,
	}
	pb, err := initPulse(&ss)
	if err != nil {
		return err
	}
	defer pb.Free()
	defer pb.Drain()

	quit := make(chan bool)
	go catMarquee(filename, quit)
	go userInput(stream, quit)

	// Number of bytes for a sample.
	go play(pb, stream, size, quit)
	select {
	case <-quit:
		return
	}
	return nil
}

func userInput(stream *flac.Stream, quit chan bool) {
	var b = make([]byte, 4)
	rightArrowKey := []byte{27, 91, 67, 0}
	leftArrowKey := []byte{27, 91, 68, 0}
	for {
		select {
		case <-quit:
			return
		default:
		}

		os.Stdin.Read(b)
		if b[0] == 'n' && stream != nil {
			close(quit)
			return
		}
		if b[0] == 'q' && stream != nil {
			os.Exit(0)
		}

		var err error
		if bytes.Equal(b, rightArrowKey) {
			err = seekForward(stream)
		}
		if bytes.Equal(b, leftArrowKey) {
			err = seekBack(stream)
		}
		if err != nil {
			logrus.Errorln(err)
		}
	}
}

func catMarquee(filename string, quit chan bool) {
	fmt.Print("                                                                      ")
	for {
		cat := []string{
			"\r(^-.-^)ﾉ - Now playing: %s",
			"\r(^._.^)ﾉ - Now playing: %s",
			"\r(^+_+^)ﾉ - Now playing: %s",
		}
		for i := 0; i < len(filename); i++ {
			select {
			case <-quit:
				return
			default:
				fmt.Printf(cat[i%len(cat)], strings.Repeat(filename+" ", 2)[i:i+20])
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
}

var globalSampleNum uint64

var globalLock sync.Mutex
func seekForward(stream *flac.Stream) error {
	logrus.Println("Seeking forward")
	globalLock.Lock()
	fmt.Println("trying to seek to", globalSampleNum+SeekSize)
	n, err := stream.Seek(globalSampleNum+SeekSize)
	fmt.Println("seeked to", n)
	fmt.Println("seeked to", n)
	globalLock.Unlock()
	logrus.Println(n/44100)
	return err
}

func seekBack(stream *flac.Stream) error {
	logrus.Println("Seeking backward")
	globalLock.Lock()
	fmt.Println("trying to seek to", globalSampleNum-SeekSize)
	n, err := stream.Seek(globalSampleNum-SeekSize)
	fmt.Println("seeked to", n)
	fmt.Println("seeked to", n)
	globalLock.Unlock()
	logrus.Println(n/44100)
	return err
}

func initPulse(ss *pulse.SampleSpec) (*pulse.Stream, error) {
	return pulse.Playback("pulse-simple test", "playback test", ss)
}

// play is the best at the moment. Plays music perfectly, with no audio
// clicks. On two channels
//
// However, 24 bits per sample flac files are unable to play both channels at
// the same time. Since I haven't been able to find a suitable SampleFormat.
func play(pb *pulse.Stream, stream *flac.Stream, size int, quit chan bool) {
	for {
		select {
		case <- quit:
			return
		default:
		}
		globalLock.Lock()
		samples, n, err := readSamplesByte(stream, size)
		globalSampleNum = n
		globalLock.Unlock()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalln(err)
		}
		pb.Write(samples)
	}
	close(quit)
}

func readSamplesByte(stream *flac.Stream, size int) ([]byte, uint64, error) {
	frame, err := stream.ParseNext()
	if err != nil {
		return nil, 0, err
	}
	samples := make([]byte, 0, int(frame.BlockSize))

	tmp := make([]byte, size)
	if stream.Info.BitsPerSample == 24 {
		for i := 0; i < int(frame.BlockSize); i++ {
			for _, sf := range frame.Subframes {
				binary.BigEndian.PutUint32(tmp, uint32(sf.Samples[i]))
				samples = append(samples, tmp...)
			}
		}
	} else {
		for i := 0; i < int(frame.BlockSize); i++ {
			for _, sf := range frame.Subframes {
				binary.BigEndian.PutUint16(tmp, uint16(sf.Samples[i]))
				samples = append(samples, tmp...)
			}
		}
	}
	return samples, frame.SampleNumber(), err
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [FILE],,,\n", os.Args[0])
	flag.PrintDefaults()
	os.Exit(1)
}
