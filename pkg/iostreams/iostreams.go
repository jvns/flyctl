// forked from https://github.com/cli/cli/tree/trunk/pkg/iostreams

package iostreams

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/cli/safeexec"
	"github.com/google/shlex"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
	"golang.org/x/crypto/ssh/terminal"
)

type IOStreams struct {
	In     io.ReadCloser
	Out    io.Writer
	ErrOut io.Writer

	// the original (non-colorable) output stream
	originalOut   io.Writer
	colorEnabled  bool
	is256enabled  bool
	terminalTheme string

	progressIndicatorEnabled bool
	progressIndicator        *spinner.Spinner

	stdinTTYOverride  bool
	stdinIsTTY        bool
	stdoutTTYOverride bool
	stdoutIsTTY       bool
	stderrTTYOverride bool
	stderrIsTTY       bool

	pagerCommand string
	pagerProcess *os.Process

	neverPrompt bool

	TempFileOverride *os.File
}

func (s *IOStreams) ColorEnabled() bool {
	return s.colorEnabled
}

func (s *IOStreams) ColorSupport256() bool {
	return s.is256enabled
}

func (s *IOStreams) DetectTerminalTheme() string {
	if !s.ColorEnabled() {
		s.terminalTheme = "none"
		return "none"
	}

	if s.pagerProcess != nil {
		s.terminalTheme = "none"
		return "none"
	}

	style := os.Getenv("GLAMOUR_STYLE")
	if style != "" && style != "auto" {
		s.terminalTheme = "none"
		return "none"
	}

	if termenv.HasDarkBackground() {
		s.terminalTheme = "dark"
		return "dark"
	}

	s.terminalTheme = "light"
	return "light"
}

func (s *IOStreams) TerminalTheme() string {
	if s.terminalTheme == "" {
		return "none"
	}

	return s.terminalTheme
}

func (s *IOStreams) SetStdinTTY(isTTY bool) {
	s.stdinTTYOverride = true
	s.stdinIsTTY = isTTY
}

func (s *IOStreams) IsStdinTTY() bool {
	if s.stdinTTYOverride {
		return s.stdinIsTTY
	}
	if stdin, ok := s.In.(*os.File); ok {
		return isTerminal(stdin)
	}
	return false
}

func (s *IOStreams) SetStdoutTTY(isTTY bool) {
	s.stdoutTTYOverride = true
	s.stdoutIsTTY = isTTY
}

func (s *IOStreams) IsStdoutTTY() bool {
	if s.stdoutTTYOverride {
		return s.stdoutIsTTY
	}
	if stdout, ok := s.Out.(*os.File); ok {
		return isTerminal(stdout)
	}
	return false
}

func (s *IOStreams) SetStderrTTY(isTTY bool) {
	s.stderrTTYOverride = true
	s.stderrIsTTY = isTTY
}

func (s *IOStreams) IsStderrTTY() bool {
	if s.stderrTTYOverride {
		return s.stderrIsTTY
	}
	if stderr, ok := s.ErrOut.(*os.File); ok {
		return isTerminal(stderr)
	}
	return false
}

func (s *IOStreams) StderrFd() uintptr {
	if f, ok := s.ErrOut.(*os.File); ok {
		return f.Fd()
	}
	return ^(uintptr(0))
}

func (s *IOStreams) StdoutFd() uintptr {
	if f, ok := s.Out.(*os.File); ok {
		return f.Fd()
	}
	return ^(uintptr(0))
}

func (s *IOStreams) IsInteractive() bool {
	return s.IsStdinTTY() && s.IsStdoutTTY()
}

func (s *IOStreams) SetPager(cmd string) {
	s.pagerCommand = cmd
}

func (s *IOStreams) StartPager() error {
	if s.pagerCommand == "" || s.pagerCommand == "cat" || !s.IsStdoutTTY() {
		return nil
	}

	pagerArgs, err := shlex.Split(s.pagerCommand)
	if err != nil {
		return err
	}

	pagerEnv := os.Environ()
	for i := len(pagerEnv) - 1; i >= 0; i-- {
		if strings.HasPrefix(pagerEnv[i], "PAGER=") {
			pagerEnv = append(pagerEnv[0:i], pagerEnv[i+1:]...)
		}
	}
	if _, ok := os.LookupEnv("LESS"); !ok {
		pagerEnv = append(pagerEnv, "LESS=FRX")
	}
	if _, ok := os.LookupEnv("LV"); !ok {
		pagerEnv = append(pagerEnv, "LV=-c")
	}

	pagerExe, err := safeexec.LookPath(pagerArgs[0])
	if err != nil {
		return err
	}
	pagerCmd := exec.Command(pagerExe, pagerArgs[1:]...)
	pagerCmd.Env = pagerEnv
	pagerCmd.Stdout = s.Out
	pagerCmd.Stderr = s.ErrOut
	pagedOut, err := pagerCmd.StdinPipe()
	if err != nil {
		return err
	}
	s.Out = pagedOut
	err = pagerCmd.Start()
	if err != nil {
		return err
	}
	s.pagerProcess = pagerCmd.Process
	return nil
}

func (s *IOStreams) StopPager() {
	if s.pagerProcess == nil {
		return
	}

	s.Out.(io.ReadCloser).Close()
	_, _ = s.pagerProcess.Wait()
	s.pagerProcess = nil
}

func (s *IOStreams) CanPrompt() bool {
	if s.neverPrompt {
		return false
	}

	return s.IsInteractive()
}

func (s *IOStreams) SetNeverPrompt(v bool) {
	s.neverPrompt = v
}

func (s *IOStreams) StartProgressIndicator() {
	s.StartProgressIndicatorMsg("")
}

func (s *IOStreams) StartProgressIndicatorMsg(msg string) {
	if !s.progressIndicatorEnabled {
		return
	}
	sp := spinner.New(spinner.CharSets[11], 250*time.Millisecond, spinner.WithWriter(s.ErrOut))
	sp.Prefix = appendMissingCharacter(msg, ' ')
	sp.Start()
	s.progressIndicator = sp
}

func (s *IOStreams) StopProgressIndicatorMsg(msg string) {
	if s.progressIndicator == nil {
		return
	}
	s.progressIndicator.FinalMSG = appendMissingCharacter(msg, newLine)
	s.progressIndicator.Stop()
	s.progressIndicator = nil
}

func (s *IOStreams) StopProgressIndicator() {
	s.StopProgressIndicatorMsg("")
}

func (s *IOStreams) ChangeProgressIndicatorMsg(msg string) {
	if s.progressIndicator == nil {
		return
	}

	s.progressIndicator.Prefix = appendMissingCharacter(msg, ' ')
}

func (s *IOStreams) TerminalWidth() int {
	defaultWidth := 80
	out := s.Out
	if s.originalOut != nil {
		out = s.originalOut
	}

	if w, _, err := terminalSize(out); err == nil {
		return w
	}

	if isCygwinTerminal(out) {
		tputExe, err := safeexec.LookPath("tput")
		if err != nil {
			return defaultWidth
		}
		tputCmd := exec.Command(tputExe, "cols")
		tputCmd.Stdin = os.Stdin
		if out, err := tputCmd.Output(); err == nil {
			if w, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
				return w
			}
		}
	}

	return defaultWidth
}

func (s *IOStreams) ColorScheme() *ColorScheme {
	return NewColorScheme(s.ColorEnabled(), s.ColorSupport256())
}

func (s *IOStreams) ReadUserFile(fn string) ([]byte, error) {
	var r io.ReadCloser
	if fn == "-" {
		r = s.In
	} else {
		var err error
		r, err = os.Open(fn)
		if err != nil {
			return nil, err
		}
	}
	defer r.Close()
	return ioutil.ReadAll(r)
}

func (s *IOStreams) TempFile(dir, pattern string) (*os.File, error) {
	if s.TempFileOverride != nil {
		return s.TempFileOverride, nil
	}
	return ioutil.TempFile(dir, pattern)
}

func System() *IOStreams {
	stdoutIsTTY := isTerminal(os.Stdout)
	stderrIsTTY := isTerminal(os.Stderr)

	pagerCommand := os.Getenv("PAGER")

	io := &IOStreams{
		In:           os.Stdin,
		originalOut:  os.Stdout,
		Out:          colorable.NewColorable(os.Stdout),
		ErrOut:       colorable.NewColorable(os.Stderr),
		colorEnabled: EnvColorForced() || (!EnvColorDisabled() && stdoutIsTTY),
		is256enabled: Is256ColorSupported(),
		pagerCommand: pagerCommand,
	}

	if stdoutIsTTY && stderrIsTTY {
		io.progressIndicatorEnabled = true
	}

	// prevent duplicate isTerminal queries now that we know the answer
	io.SetStdoutTTY(stdoutIsTTY)
	io.SetStderrTTY(stderrIsTTY)
	return io
}

func Test() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{
		In:     ioutil.NopCloser(in),
		Out:    out,
		ErrOut: errOut,
	}, in, out, errOut
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func isCygwinTerminal(w io.Writer) bool {
	if f, isFile := w.(*os.File); isFile {
		return isatty.IsCygwinTerminal(f.Fd())
	}
	return false
}

func terminalSize(w io.Writer) (int, int, error) {
	if f, isFile := w.(*os.File); isFile {
		return terminal.GetSize(int(f.Fd()))
	}
	return 0, 0, fmt.Errorf("%v is not a file", w)
}

const newLine = '\n'

func appendMissingCharacter(msg string, char byte) string {
	buff := []byte(msg)
	if len(buff) == 0 || buff[len(buff)-1] != char {
		buff = append(buff, char)
	}
	return string(buff)
}