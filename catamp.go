// It really whips the cat's ass.
//
//
// $ catamp *.flac
//
// Remote pausing is done with SIGTSTP
// $ ps -C catamp | tail -n 1 | awk '{print $1}' | xargs -I '{}' kill -s SIGTSTP '{}'
//
// Remote playing is done with SIGCONT
// $ ps -C catamp | tail -n 1 | awk '{print $1}' | xargs -I '{}' kill -s SIGCONT '{}'
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/karlek/profile"
	"github.com/llgcode/draw2d/draw2dbase"
	"github.com/mesilliac/pulse-simple"
	"github.com/mewkiz/flac"
	"github.com/mewmew/sdl/win"
	"github.com/mewmew/we"
)

var (
	width, height = 1280, 720
)

func main() {
	defer profile.Start(profile.CPUProfile).Stop()
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
	}

	// disable input buffering
	exec.Command("stty", "-F", "/dev/tty", "cbreak", "min", "1").Run()
	// do not display entered characters on the screen
	exec.Command("stty", "-F", "/dev/tty", "-echo").Run()
	// restore the echoing state when exiting
	defer exec.Command("stty", "-F", "/dev/tty", "echo").Run()

	for _, f := range flag.Args() {
		if err := catamp(f); err != nil {
			log.Fatalln(err)
		}
	}
}

func catamp(filename string) (err error) {
	stream, err := flac.ParseFile(filename)
	if err != nil {
		fmt.Println(filename)
		return err
	}
	defer stream.Close()

	// Number of bytes for a sample. Used by semi.
	var size int

	var sf pulse.SampleFormat
	if stream.Info.BitsPerSample == 16 {
		sf = pulse.SAMPLE_S24_32LE
		size = 4
	} else if stream.Info.BitsPerSample == 24 {
		logrus.Warnln("not implemented - only one channel used")
		sf = pulse.SAMPLE_S24_32BE
		size = 8
	}
	fmt.Println(stream.Info.BitsPerSample)
	ss := pulse.SampleSpec{Format: sf, Rate: stream.Info.SampleRate, Channels: stream.Info.NChannels}
	pb, err := initPulse(&ss)
	if err != nil {
		return err
	}
	defer pb.Free()
	defer pb.Drain()

	quit := make(chan bool)
	go func() {
		var b = []byte{0x00}
		go func(quit chan bool) {
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
						fmt.Printf(cat[i%len(cat)], strings.Repeat(filename+" ", 2)[i:i+len(filename)])
						time.Sleep(200 * time.Millisecond)
					}
				}
			}
		}(quit)
		for {
			select {
			case <-quit:
				return
			default:
				os.Stdin.Read(b)
				if b[0] == 'n' && stream != nil {
					close(quit)
					stream.Seek(0, io.SeekEnd)
					return
				}
			}
		}
	}()
	drawMusic(pb, stream, size)
	// play(pb, stream, size)
	select {
	case <-quit:
	default:
		close(quit)
	}
	return nil
}

func initPulse(ss *pulse.SampleSpec) (*pulse.Stream, error) {
	return pulse.Playback("pulse-simple test", "playback test", ss)
}

// play is the best at the moment. Plays music perfectly, with no audio
// clicks. On two channels
//
// However, 24 bits per sample flac files are unable to play both channels at
// the same time. Since I haven't been able to find a suitable SampleFormat.
func play(pb *pulse.Stream, stream *flac.Stream, size int) {
	var samples = []byte{}
	for {
		n, err := readSamplesByte(stream, &samples, size)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalln(err)
		}
		pb.Write(samples[:n])
		samples = samples[n:]
	}
}

func readSamplesByte(stream *flac.Stream, samples *[]byte, size int) (n int, err error) {
	tmp := make([]byte, size)
	for {
		frame, err := stream.ParseNext()
		if err != nil {
			return n, err
		}
		if stream.Info.BitsPerSample == 24 {
			for i := 0; i < int(frame.BlockSize); i++ {
				binary.BigEndian.PutUint32(tmp, uint32(frame.Subframes[0].Samples[i]))
				*samples = append(*samples, tmp...)
			}
		} else {
			for i := 0; i < int(frame.BlockSize); i++ {
				for _, sf := range frame.Subframes {
					binary.BigEndian.PutUint32(tmp, uint32(sf.Samples[i]))
					*samples = append(*samples, tmp...)
				}
			}
		}
		if len(*samples) >= int(stream.Info.SampleRate)*size {
			break
		}
	}
	n = int(stream.Info.SampleRate) * size
	if len(*samples) < n {
		n = len(*samples)
	}
	return n, err
}

func readSamplesInt(stream *flac.Stream, samples *[]int32, read int) (n int, err error) {
	for {
		frame, err := stream.ParseNext()
		if err != nil {
			return n, err
		}
		if stream.Info.BitsPerSample == 24 {
			for i := 0; i < int(frame.BlockSize); i++ {
				*samples = append(*samples, frame.Subframes[0].Samples[i])
			}
		} else {
			for i := 0; i < int(frame.BlockSize); i++ {
				for _, sf := range frame.Subframes {
					*samples = append(*samples, int32(sf.Samples[i]))
				}
			}
		}
		if len(*samples) >= read {
			break
		}
	}
	n = read
	if len(*samples) < n {
		n = len(*samples)
	}
	return n, err
}

func drawMusic(pb *pulse.Stream, stream *flac.Stream, size int) {
	var samples = []int32{}

	var max int
	if size == 4 {
		max = math.MaxInt16
	} else if size == 8 {
		max = math.MaxInt32 / 128
	}
	tmp := make([]byte, size)
	read := int(stream.Info.BlockSizeMax)
	// read := int(stream.Info.SampleRate / 8)
	var n int
	var err error
	log.Println(read, stream.Info)
	// if read < int(stream.Info.BlockSizeMin) {
	// 	log.Fatalln("bad read size")
	// }

	// Open the window.
	err = win.Open(width, height, win.Resizeable)
	if err != nil {
		log.Fatalln(err)
	}
	defer win.Close()

	normalized := make([]float64, int(stream.Info.SampleRate))
	for {
		var data = []byte{}
		if len(samples) < read {
			n, err = readSamplesInt(stream, &samples, read)
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Fatalln(err)
			}
		} else {
			n = read
		}
		for i, s := range samples[:n] {
			normalized[i] = float64(s) / float64(max)
			binary.BigEndian.PutUint32(tmp, uint32(s))
			data = append(data, tmp...)
		}
		samples = samples[n:]

		// Responsive window size.
		lolfi, _ := win.Screen()
		width, height = lolfi.Width, lolfi.Height
		img := image.NewRGBA(image.Rectangle{image.Point{0, 0}, image.Point{width, height}})
		draw.Draw(img, img.Bounds(), image.Black, image.ZP, draw.Src)

		wg := new(sync.WaitGroup)
		wg.Add(1)
		go func(wg *sync.WaitGroup) {
			v0 := normalized[0]
			for i := 2; i < n; i += 2 {
				line(img, normalized[i], &v0, i, read, width, height)
			}
			wg.Done()
		}(wg)
		wg.Add(1)
		go func(wg *sync.WaitGroup) {
			v0 := normalized[1]
			for i := 3; i < n; i += 2 {
				line(img, normalized[i], &v0, i, read, width, height)
			}
			wg.Done()
		}(wg)
		wg.Wait()

		// Change image.Image type to win.Image.
		im, err := win.ReadImage(img)
		if err != nil {
			log.Fatalln(err)
		}

		// Draw image to window.
		err = win.Draw(image.ZP, im)
		if err != nil {
			log.Fatalln(err)
		}
		// Display window updates on screen.
		win.Update()
		im.Free()

		pb.Write(data)
		data = data[n*size:]

		// Poll events until the event queue is empty.
		for e := win.PollEvent(); e != nil; e = win.PollEvent() {
			switch e.(type) {
			case we.Close:
				os.Exit(0)
			}
		}
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [FILE],,,\n", os.Args[0])
	flag.PrintDefaults()
	os.Exit(1)
}

func r255() uint8 {
	return uint8(rand.Intn(255))
}

func line(img draw.Image, v float64, v0 *float64, i, read, width, height int) {
	// for i := 0; i < width; i++ {
	// x := v*100 + math.Min(float64(width), float64(height))/5*math.Cos(v*float64(i)) + float64(width/2)
	// y := math.Min(float64(width), float64(height))/5*math.Sin(v*float64(i)) + float64(height/2)
	// x0 := (*v0)*100 + math.Min(float64(width), float64(height))/5*math.Cos((*v0)*float64(i-2)) + float64(width/2)
	// y0 := (*v0)*100 + math.Min(float64(width), float64(height))/5*math.Sin((*v0)*float64(i-2)) + float64(height/2)
	// img.Set(int(x), int(y), c)
	// }
	x, y := math.Floor(float64(i)/float64(read)*float64(width)), math.Floor(v*float64(height)/2+float64(height)/2)
	x0, y0 := math.Floor(float64(i-2)/float64(read)*float64(width)), math.Floor((*v0)*float64(height)/2+float64(height)/2)
	// c := color.RGBA{r255(), r255(), r255(), 255}
	c := color.RGBA{uint8((float64(x) / float64(width)) * 255), 128, 190, 255}
	draw2dbase.Bresenham(img, c, int(x0), int(y0), int(x), int(y))
	img.Set(int(x), int(y), c)
	draw2dbase.Bresenham(img, c, int(x0), int(y0), int(x), int(y))
	(*v0) = v
}

func circle(img draw.Image, v float64, v0 *float64, i, read, width, height int) {

	// for i := 0; i < width; i++ {
	x := v*100 + math.Min(float64(width), float64(height))/5*math.Cos(v*float64(i)) + float64(width/2)
	y := math.Min(float64(width), float64(height))/5*math.Sin(v*float64(i)) + float64(height/2)
	c := color.RGBA{uint8((float64(x) / float64(width)) * 255), 128, 190, 255}
	// x0 := (*v0)*100 + math.Min(float64(width), float64(height))/5*math.Cos((*v0)*float64(i-2)) + float64(width/2)
	// y0 := (*v0)*100 + math.Min(float64(width), float64(height))/5*math.Sin((*v0)*float64(i-2)) + float64(height/2)
	img.Set(int(x), int(y), c)
	// }
	// x = float64(width)*math.Cos(float64(i)/float64(read)) - float64(width/2)
	// y := float64(height)*math.Sin(v*float64(height)) + float64(height/2)
	// fmt.Println(x, y, v, v/float64(height))
	// x0 := float64(width) * math.Acos((float64(i-2) / float64(read))) / math.Pi
	// y0 := float64(height) * math.Asin(math.Floor((*v0)*float64(height)/2)) / math.Pi
	// x, y := math.Floor(float64(i)/float64(read)*float64(width)), math.Floor(v*float64(height)/2+float64(height)/2)
	// x0, y0 := math.Floor(float64(i-2)/float64(read)*float64(width)), math.Floor((*v0)*float64(height)/2+float64(height)/2)
	// c := color.RGBA{r255(), r255(), r255(), 255}
	// draw2dbase.Bresenham(img, c, int(x0), int(y0), int(x), int(y))
	// img.Set(int(x), int(y), c)
	// draw2dbase.Bresenham(img, c, int(x0), int(y0), int(x), int(y))
	// (*v0) = v
}
