package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// PickerMode is the active mode of the file picker.
type PickerMode string

const (
	PickerOpen PickerMode = "open"
	PickerSave PickerMode = "save"
)

// dimSGR returns the ANSI dim SGR code, or empty string in monochrome mode.
func dimSGR() string {
	if theme.IsMonochrome() {
		return ""
	}
	return "\x1b[2m"
}

func pickerReset() string {
	if theme.IsMonochrome() {
		return ""
	}
	return "\x1b[0m"
}

// pickerTint wraps s in the named theme foreground color. Respects NO_COLOR.
func pickerTint(s, color string) string {
	if theme.IsMonochrome() {
		return s
	}
	sgr := theme.ColorSGR(color, theme.Fg)
	if sgr == "" {
		return s
	}
	return sgr + s + theme.AnsiReset
}

// filePickerSearchState mirrors railSearchState for the file picker listing.
type filePickerSearchState struct {
	query     string
	smartCase bool
	matches   []pickerMatch
	current   int
	truncated bool
}

type pickerMatch struct {
	RowIndex  int
	ByteStart int
	ByteEnd   int
}

type pickerInputKind int

const (
	inputKindNone     pickerInputKind = iota
	inputKindFilename                 // editing the save-as filename
	inputKindSearch                   // typing a search query for the listing
	inputKindNewDir                   // typing a new directory name
)

// FilePickerContext renders the filesystem path picker as a centered
// TEMPORARY_POPUP with three zones: breadcrumb, directory listing, and
// (in save mode) a filename input footer.
type FilePickerContext struct {
	BaseContext

	deps  Deps
	modes types.ModeSetter
	view  types.View
	fs    afero.Fs

	mode         PickerMode
	currentPath  string
	items        []models.FSEntry
	cursor       int
	search       filePickerSearchState
	showHidden   bool
	filename     string
	inputFocused bool
	inputKind    pickerInputKind
	errMsg       string

	sortOrder int
	nowFunc   func() time.Time

	listingOffset int

	onConfirm func(string)
	onCancel  func()
	popFn     func() error

	viewW int
	viewH int
}

// NewFilePickerContext builds a FilePickerContext bound to FILE_PICKER.
func NewFilePickerContext(base BaseContext, deps Deps) *FilePickerContext {
	return &FilePickerContext{BaseContext: base, deps: deps, nowFunc: time.Now}
}

// SetFs installs the filesystem abstraction. Must be called before Push.
func (f *FilePickerContext) SetFs(fs afero.Fs) { f.fs = fs }

// Push initializes the picker state, navigates to startPath (or home),
// and stores callbacks + pop closure. startPath may be "" to use the
// persisted directory or home.
func (f *FilePickerContext) Push(mode PickerMode, startPath string, onConfirm func(string), onCancel func(), popFn func() error) {
	f.mode = mode
	f.onConfirm = onConfirm
	f.onCancel = onCancel
	f.popFn = popFn
	f.cursor = 0
	f.search = filePickerSearchState{}
	f.showHidden = false
	f.filename = ""
	f.inputFocused = false
	f.inputKind = inputKindNone
	f.errMsg = ""
	f.listingOffset = 0
	f.sortOrder = 0
	if f.nowFunc == nil {
		f.nowFunc = time.Now
	}
	if mode == PickerSave && startPath != "" {
		base := filepath.Base(startPath)
		if base != "." && base != string(filepath.Separator) && base != "" {
			f.filename = base
		}
	}
	f.NavigateTo(startPath)
}

// NavigateTo resolves path (or home if empty) and lists its entries.
func (f *FilePickerContext) NavigateTo(path string) {
	if path == "" || f.fs == nil {
		return
	}
	resolved := resolvePath(f.fs, path)
	if resolved == "" {
		f.errMsg = "Cannot access: " + path
		return
	}
	f.currentPath = resolved
	f.errMsg = ""
	f.listingOffset = 0
	entries, err := afero.ReadDir(f.fs, resolved)
	if err != nil {
		f.errMsg = "Read error: " + err.Error()
		f.items = nil
		f.cursor = 0
		return
	}
	f.items = make([]models.FSEntry, 0, len(entries))
	lst, lstOk := f.fs.(afero.Lstater)
	for _, e := range entries {
		if !f.showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isSymlink := false
		if lstOk {
			entryPath := filepath.Join(resolved, e.Name())
			info, _, lerr := lst.LstatIfPossible(entryPath)
			if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
				isSymlink = true
			}
		}
		f.items = append(f.items, models.FSEntry{
			Name:      e.Name(),
			Path:      filepath.Join(resolved, e.Name()),
			IsDir:     e.IsDir(),
			IsSymlink: isSymlink,
			SizeBytes: e.Size(),
			ModTime:   e.ModTime(),
		})
	}
	sort.Slice(f.items, func(i, j int) bool {
		if f.items[i].IsDir != f.items[j].IsDir {
			return f.items[i].IsDir
		}
		switch f.sortOrder {
		case 1: // size descending
			if f.items[i].SizeBytes != f.items[j].SizeBytes {
				return f.items[i].SizeBytes > f.items[j].SizeBytes
			}
		case 2: // modified descending
			if !f.items[i].ModTime.Equal(f.items[j].ModTime) {
				return f.items[i].ModTime.After(f.items[j].ModTime)
			}
		}
		return strings.ToLower(f.items[i].Name) < strings.ToLower(f.items[j].Name)
	})
	if f.cursor >= len(f.items) {
		if len(f.items) == 0 {
			f.cursor = 0
		} else {
			f.cursor = len(f.items) - 1
		}
	}
}

// resolvePath cleans and resolves path to an absolute, existing directory.
// Returns "" when the path cannot be resolved.
func resolvePath(fs afero.Fs, path string) string {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return ""
	}
	fi, err := fs.Stat(abs)
	if err == nil {
		if fi.IsDir() {
			return abs
		}
		parent := filepath.Dir(abs)
		fi, err = fs.Stat(parent)
		if err == nil && fi.IsDir() {
			return parent
		}
		return ""
	}
	parent := filepath.Dir(abs)
	fi, err = fs.Stat(parent)
	if err == nil && fi.IsDir() {
		return parent
	}
	return ""
}

// Refresh re-lists the current directory. Used after creating a directory
// or any time the listing may be stale.
func (f *FilePickerContext) Refresh() {
	f.NavigateTo(f.currentPath)
}

// Selected returns the FSEntry under the cursor, or zero value when empty.
func (f *FilePickerContext) Selected() models.FSEntry {
	if f.cursor < 0 || f.cursor >= len(f.items) {
		return models.FSEntry{}
	}
	return f.items[f.cursor]
}

// MoveCursor shifts the cursor by delta, clamping to valid range.
func (f *FilePickerContext) MoveCursor(delta int) {
	f.SetCursor(f.cursor + delta)
}

// SetCursor moves the cursor to i, clamping.
func (f *FilePickerContext) SetCursor(i int) {
	if len(f.items) == 0 {
		f.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(f.items) {
		i = len(f.items) - 1
	}
	f.cursor = i
}

// Descend enters the selected directory. No-op when the selected entry
// is not a directory or when items is empty.
func (f *FilePickerContext) Descend() {
	sel := f.Selected()
	if sel.IsDir {
		f.NavigateTo(sel.Path)
	}
}

// Ascend moves to the parent directory. No-op at root.
func (f *FilePickerContext) Ascend() {
	parent := filepath.Dir(f.currentPath)
	if parent != f.currentPath {
		f.NavigateTo(parent)
	}
}

// Confirm invokes the stored onConfirm callback (if any) with the selected
// file path (or typed filename in save mode), then pops the picker.
func (f *FilePickerContext) Confirm() {
	path := f.resolveConfirmPath()
	cb := f.onConfirm
	cancel := f.onCancel
	pop := f.popFn
	f.onConfirm = nil
	f.onCancel = nil
	f.popFn = nil
	if pop != nil {
		_ = pop()
	}
	if path != "" {
		if cb != nil {
			cb(path)
		}
	} else if cancel != nil {
		cancel()
	}
}

// Cancel invokes the onCancel callback (if any) and pops the picker.
func (f *FilePickerContext) Cancel() {
	cb := f.onCancel
	pop := f.popFn
	f.onConfirm = nil
	f.onCancel = nil
	f.popFn = nil
	if pop != nil {
		_ = pop()
	}
	if cb != nil {
		cb()
	}
}

// resolveConfirmPath returns the absolute path to confirm:
// in save mode with input focused, joins filename to current directory;
// otherwise returns Selected().Path (if file) or "".
func (f *FilePickerContext) resolveConfirmPath() string {
	if f.mode == PickerSave && f.inputFocused {
		name := f.Buffer()
		if name == "" {
			return ""
		}
		return filepath.Join(f.currentPath, name)
	}
	sel := f.Selected()
	if sel.Path == "" || sel.IsDir {
		return ""
	}
	return sel.Path
}

// ToggleHidden flips the show-hidden flag and refreshes the listing.
func (f *FilePickerContext) ToggleHidden() {
	saved := f.Selected().Path
	f.showHidden = !f.showHidden
	f.Refresh()
	if saved != "" {
		for i, item := range f.items {
			if item.Path == saved {
				f.cursor = i
				return
			}
		}
	}
}

// CycleSort cycles through sort orders: name → size → modified → name.
func (f *FilePickerContext) CycleSort() {
	f.sortOrder = (f.sortOrder + 1) % 3
	f.ClearSearch()
	f.Refresh()
}

// NavigateHome navigates to the current user's home directory.
func (f *FilePickerContext) NavigateHome() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}
	f.NavigateTo(home)
}

// SetView records the gocui view the layout pass passes in each frame.
func (f *FilePickerContext) SetView(v types.View) { f.view = v }

// SetModes records the mode setter for focus/blur hooks. Nil-safe.
func (f *FilePickerContext) SetModes(m types.ModeSetter) { f.modes = m }

// Buffer returns the text from the gocui view's TextArea when input is
// focused, or the stored filename otherwise.
func (f *FilePickerContext) Buffer() string {
	if f.view != nil && f.view.TextArea != nil && f.inputFocused {
		return f.view.TextArea.GetContent()
	}
	return f.filename
}

// ClearViewBuffer clears the view's TextArea.
func (f *FilePickerContext) ClearViewBuffer() {
	if f.view != nil && f.view.TextArea != nil {
		f.view.TextArea.Clear()
	}
}

// HandleFocus sets the mode to Command or Insert based on whether the
// filename input is focused. The terminal caret is enabled only in
// filename-input mode (Insert) so printable characters reach the TextArea.
// On initial focus with input pre-focused (save mode), it seeds the
// TextArea with the pre-filled filename (ToggleInputFocus handles toggles;
// HandleFocus covers the initial push).
func (f *FilePickerContext) HandleFocus(_ types.OnFocusOpts) error {
	if f.modes != nil {
		if f.inputFocused {
			f.modes.Set(types.FILE_PICKER, types.ModeInsert)
		} else {
			f.modes.Set(types.FILE_PICKER, types.ModeCommand)
		}
	}
	if f.deps.GuiDriver != nil {
		f.deps.GuiDriver.SetCaretEnabled(f.inputFocused)
	}
	if f.inputFocused && f.view != nil && f.view.TextArea != nil {
		f.view.TextArea.Clear()
		f.view.TextArea.TypeString(f.filename)
	}
	return nil
}

// HandleFocusLost clears the mode, view, and filename, and disables the
// terminal caret.
func (f *FilePickerContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if f.modes != nil {
		f.modes.Reset(types.FILE_PICKER)
	}
	if f.deps.GuiDriver != nil {
		f.deps.GuiDriver.SetCaretEnabled(false)
	}
	f.view = nil
	return nil
}

// Items returns the current directory entries.
func (f *FilePickerContext) Items() []models.FSEntry { return f.items }

// InputFocused reports whether the filename input has focus.
func (f *FilePickerContext) InputFocused() bool { return f.inputFocused }

// InputKind reports what purpose the focused text input serves.
func (f *FilePickerContext) InputKind() pickerInputKind { return f.inputKind }

// activateInput clears the TextArea, seeds it, focuses, and sets the
// given kind. Caller must call HandleRender afterwards.
func (f *FilePickerContext) activateInput(k pickerInputKind) {
	f.inputKind = k
	if !f.inputFocused {
		f.ClearViewBuffer()
		if f.view != nil && f.view.TextArea != nil {
			f.view.TextArea.TypeString(f.filename)
		}
		if f.deps.GuiDriver != nil {
			f.deps.GuiDriver.SetCaretEnabled(true)
		}
		if f.modes != nil {
			f.modes.Set(types.FILE_PICKER, types.ModeInsert)
		}
		f.inputFocused = true
	}
}

// deactivateInput returns focus to the directory listing, saving the
// current TextArea content into filename.
func (f *FilePickerContext) deactivateInput() {
	if f.view != nil && f.view.TextArea != nil {
		f.filename = f.view.TextArea.GetContent()
	}
	if f.deps.GuiDriver != nil {
		f.deps.GuiDriver.SetCaretEnabled(false)
	}
	if f.modes != nil {
		f.modes.Set(types.FILE_PICKER, types.ModeCommand)
	}
	f.inputFocused = false
	f.inputKind = inputKindNone
}

// ActivateSearch clears the text area, sets search mode, and focuses
// the input so the user can type a listing-filter query inline.
func (f *FilePickerContext) ActivateSearch() {
	f.activateInput(inputKindSearch)
}

// ApplySearch reads the input text, calls SetSearch, and returns focus
// to the directory listing.
func (f *FilePickerContext) ApplySearch() {
	if f.inputFocused && f.inputKind == inputKindSearch {
		query := ""
		if f.view != nil && f.view.TextArea != nil {
			query = f.view.TextArea.GetContent()
		}
		f.deactivateInput()
		f.SetSearch(query)
		return
	}
	f.deactivateInput()
}

// CancelSearch discards the input, clears the active search resetting
// the listing, and returns focus to the listing.
func (f *FilePickerContext) CancelSearch() {
	if f.inputFocused && f.inputKind == inputKindSearch {
		f.deactivateInput()
		f.ClearSearch()
		return
	}
	f.deactivateInput()
}

// ActivateNewDir clears the text area, sets new-dir mode, and focuses
// the input.
func (f *FilePickerContext) ActivateNewDir() {
	f.activateInput(inputKindNewDir)
}

// ApplyNewDir creates a directory with the typed name and returns focus
// to the listing.
func (f *FilePickerContext) ApplyNewDir() {
	if f.inputFocused && f.inputKind == inputKindNewDir {
		name := ""
		if f.view != nil && f.view.TextArea != nil {
			name = f.view.TextArea.GetContent()
		}
		f.deactivateInput()
		if name != "" {
			f.CreateDirectory(name)
		}
		return
	}
	f.deactivateInput()
}

// CancelNewDir discards the input and returns focus to the listing.
func (f *FilePickerContext) CancelNewDir() {
	f.deactivateInput()
}

// SearchInputActive reports whether the input is in search mode.
func (f *FilePickerContext) SearchInputActive() bool {
	return f.inputFocused && f.inputKind == inputKindSearch
}

// NewDirInputActive reports whether the input is in new-directory mode.
func (f *FilePickerContext) NewDirInputActive() bool {
	return f.inputFocused && f.inputKind == inputKindNewDir
}

// CurrentPath returns the current directory path.
func (f *FilePickerContext) CurrentPath() string { return f.currentPath }

// ToggleInputFocus switches focus between the directory listing and the
// filename input. No-op in open mode. Syncs filename with TextArea,
// toggles the terminal caret, and switches between ModeInsert (typing)
// and ModeCommand (listing navigation).
func (f *FilePickerContext) ToggleInputFocus() {
	if f.mode != PickerSave {
		return
	}
	if f.inputFocused {
		f.deactivateInput()
	} else {
		f.inputKind = inputKindFilename
		f.ClearViewBuffer()
		if f.view != nil && f.view.TextArea != nil {
			f.view.TextArea.TypeString(f.filename)
		}
		if f.deps.GuiDriver != nil {
			f.deps.GuiDriver.SetCaretEnabled(true)
		}
		if f.modes != nil {
			f.modes.Set(types.FILE_PICKER, types.ModeInsert)
		}
		f.inputFocused = true
	}
}

// SetFilename sets the filename input text.
func (f *FilePickerContext) SetFilename(s string) { f.filename = s }

// AppendFilenameChar appends a rune to the filename input.
func (f *FilePickerContext) AppendFilenameChar(r rune) {
	f.filename += string(r)
}

// DeleteFilenameChar removes the last rune from the filename input.
func (f *FilePickerContext) DeleteFilenameChar() {
	runes := []rune(f.filename)
	if len(runes) > 0 {
		f.filename = string(runes[:len(runes)-1])
	}
}

// CreateDirectory creates a new directory inside currentPath. Name comes
// from the filename input when inputFocused, or uses the selected item
// name as a prompt seed. In save mode, this also invokes a prompt-like
// flow — but for simplicity, we create a directory named from the
// filename input.
func (f *FilePickerContext) CreateDirectory(name string) {
	if name == "" || f.fs == nil {
		return
	}
	newPath := filepath.Join(f.currentPath, name)
	if err := f.fs.Mkdir(newPath, 0o755); err != nil {
		f.errMsg = "Cannot create: " + err.Error()
		return
	}
	f.Refresh()
}

// SetSearch installs the search query and recomputes matches.
func (f *FilePickerContext) SetSearch(query string) {
	if query == "" {
		f.search = filePickerSearchState{}
		return
	}
	smartCase := railQueryIsCaseSensitive(query)
	matches := make([]pickerMatch, 0)
	truncated := false
	for i, item := range f.items {
		for _, span := range railSubstringMatches(item.Name, query, smartCase) {
			if len(matches) >= 200 {
				truncated = true
				break
			}
			matches = append(matches, pickerMatch{
				RowIndex:  i,
				ByteStart: span[0],
				ByteEnd:   span[1],
			})
		}
		if truncated {
			break
		}
	}
	current := pickerFirstMatchAtOrAfter(matches, f.cursor)
	f.search = filePickerSearchState{
		query:     query,
		smartCase: smartCase,
		matches:   matches,
		current:   current,
		truncated: truncated,
	}
	if len(matches) > 0 && current >= 0 && current < len(matches) {
		f.SetCursor(matches[current].RowIndex)
	}
}

// pickerFirstMatchAtOrAfter mirrors railFirstMatchAtOrAfter for pickerMatch.
func pickerFirstMatchAtOrAfter(matches []pickerMatch, from int) int {
	for i, m := range matches {
		if m.RowIndex >= from {
			return i
		}
	}
	return 0
}

// NextMatch advances the search match by one.
func (f *FilePickerContext) NextMatch() {
	f.stepMatch(+1)
}

// PrevMatch moves the search match back by one.
func (f *FilePickerContext) PrevMatch() {
	f.stepMatch(-1)
}

func (f *FilePickerContext) stepMatch(dir int) {
	n := len(f.search.matches)
	if n == 0 {
		return
	}
	f.search.current = ((f.search.current+dir)%n + n) % n
	m := f.search.matches[f.search.current]
	f.SetCursor(m.RowIndex)
}

// ClearSearch drops the active search.
func (f *FilePickerContext) ClearSearch() { f.search = filePickerSearchState{} }

// SearchActive reports whether a search is active.
func (f *FilePickerContext) SearchActive() bool { return f.search.query != "" }

// SearchStatus reports the 1-based match index, total count, and whether
// results were truncated.
func (f *FilePickerContext) SearchStatus() (cur, total int, truncated bool) {
	total = len(f.search.matches)
	if total > 0 && f.search.current >= 0 && f.search.current < total {
		cur = f.search.current + 1
	}
	truncated = f.search.truncated
	return
}

// SetViewSize records the viewport dimensions the layout pass computed.
func (f *FilePickerContext) SetViewSize(w, h int) { f.viewW = w; f.viewH = h }

// maxVisibleItems returns the number of listing entries that can fit
// within the current viewport after reserving space for headers and footer.
func (f *FilePickerContext) maxVisibleItems() int {
	innerH := 20
	if f.view != nil {
		if h := f.view.InnerHeight(); h > 0 {
			innerH = h
		}
	}
	headerLines := 1
	if f.errMsg != "" {
		headerLines++
	}
	footerLines := f.footerLineCount()
	maxVisible := innerH - headerLines - footerLines
	if maxVisible < 1 {
		maxVisible = 1
	}
	return maxVisible
}

// HandleRender computes the visible listing window from the view height,
// adjusts the listing offset to keep the cursor visible, renders the
// body, and writes it to the view. No gocui scroll needed — the rendering
// fits exactly into the viewport with the footer pinned to the bottom.
func (f *FilePickerContext) HandleRender() error {
	headerLines := 1
	if f.errMsg != "" {
		headerLines++
	}
	maxVisible := f.maxVisibleItems()

	if len(f.items) > 0 {
		if f.cursor < f.listingOffset {
			f.listingOffset = f.cursor
		}
		if f.cursor >= f.listingOffset+maxVisible {
			f.listingOffset = f.cursor - maxVisible + 1
		}
		if f.listingOffset < 0 {
			f.listingOffset = 0
		}
		if f.listingOffset+maxVisible > len(f.items) {
			f.listingOffset = len(f.items) - maxVisible
			if f.listingOffset < 0 {
				f.listingOffset = 0
			}
		}
	}

	body := f.RenderBody()
	viewName := f.GetViewName()
	writeView(f.deps, func() error {
		return f.deps.GuiDriver.SetContent(viewName, body)
	})

	if f.inputFocused && f.view != nil && f.view.TextArea != nil && f.deps.GuiDriver != nil {
		cursorX, _ := f.view.TextArea.GetCursorXY()
		caretX := cursorX
		switch f.inputKind {
		case inputKindSearch:
			caretX += 10
		case inputKindNewDir:
			caretX += 17
		default:
			caretX += 13
		}
		caretY := headerLines + maxVisible
		_ = f.deps.GuiDriver.SetViewCursor(viewName, caretX, caretY)
	}

	return nil
}

// RenderBody returns the fully assembled picker body string.
// The listing is rendered as a window into the full item set using
// listingOffset; empty lines pad the remaining space so the save-mode
// footer is pinned to the bottom of the viewport.
func (f *FilePickerContext) RenderBody() string {
	var b strings.Builder

	b.WriteString(f.renderBreadcrumb())
	b.WriteByte('\n')

	if f.errMsg != "" {
		b.WriteString(pickerTint(f.errMsg, theme.Current().Error.Fg))
		b.WriteByte('\n')
	}

	maxVisible := f.maxVisibleItems()

	end := f.listingOffset + maxVisible
	if end > len(f.items) {
		end = len(f.items)
	}
	rendered := end - f.listingOffset

	b.WriteString(f.renderListing(f.listingOffset, end))
	b.WriteByte('\n')

	// Pad remaining lines so the footer stays at the bottom of the view.
	for i := rendered; i < maxVisible; i++ {
		b.WriteByte('\n')
	}

	b.WriteString(f.renderFooter())

	return b.String()
}

func (f *FilePickerContext) renderBreadcrumb() string {
	modeLabel := "Open"
	if f.mode == PickerSave {
		modeLabel = "Save"
	}
	path := grid.SanitizeCellEscapes(f.currentPath)
	suffix := ""
	if f.showHidden {
		suffix = " [H]"
	}
	full := modeLabel + ":  " + dimSGR() + path + suffix + pickerReset()

	// Left-truncation if total exceeds view width
	maxW := f.viewW - 2
	if maxW > 0 && len(full) > maxW {
		// Preserve at least 3 rightmost components
		parts := strings.Split(path, string(filepath.Separator))
		keep := parts
		if len(parts) > 3 {
			keep = parts[len(parts)-3:]
		}
		truncPath := "..." + strings.Join(keep, string(filepath.Separator))
		truncFull := modeLabel + ":  " + dimSGR() + truncPath + suffix + pickerReset()
		// If still too long, show just "..."
		for len(truncFull) > maxW && len(keep) > 0 {
			keep = keep[1:]
			truncPath = "..." + strings.Join(keep, string(filepath.Separator))
			truncFull = modeLabel + ":  " + dimSGR() + truncPath + suffix + pickerReset()
		}
		if len(truncFull) > maxW {
			truncFull = modeLabel + ":  " + dimSGR() + "..." + suffix + pickerReset()
		}
		return truncFull
	}
	return full
}

func (f *FilePickerContext) renderListing(start, end int) string {
	if len(f.items) == 0 {
		if f.errMsg == "" {
			if start == 0 {
				return "  " + dimSGR() + "(empty directory)" + pickerReset()
			}
		}
		return ""
	}
	if start < 0 {
		start = 0
	}
	if end > len(f.items) {
		end = len(f.items)
	}
	if start >= end {
		return ""
	}
	var b strings.Builder
	maxNameW := f.maxNameWidth()
	avail := 20
	if f.view != nil {
		if iw := f.view.InnerWidth(); iw > 4 {
			avail = iw - 4
		}
	}
	if avail < 20 {
		avail = 20
	}
	nowFunc := f.nowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()
	t := theme.Current()
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		item := f.items[i]
		isCursor := i == f.cursor && !f.inputFocused
		if isCursor {
			b.WriteString(pickerTint("> ", t.PopupBorder.Fg))
		} else {
			b.WriteString("  ")
		}

		name := grid.SanitizeCellEscapes(item.Name)

		indicator := ""
		if item.IsSymlink && item.IsDir {
			indicator = "@/"
		} else if item.IsSymlink {
			indicator = "@"
		} else if item.IsDir {
			indicator = "/"
		}

		isSQL := strings.HasSuffix(strings.ToLower(item.Name), ".sql")
		dimmed := f.mode == PickerOpen && !item.IsDir && !isSQL

		nameColor := ""
		if item.IsDir {
			nameColor = t.Info.Fg
		}

		display := truncateDisplay(name, indicator, maxNameW, avail)
		if isCursor {
			b.WriteString(pickerTint(display, t.PopupBorder.Fg))
		} else if item.IsDir {
			b.WriteString(pickerTint(display, nameColor))
		} else if dimmed {
			b.WriteString(dimSGR() + display + pickerReset())
		} else {
			b.WriteString(display)
		}

		if !item.IsDir {
			sizeStr := formatSize(item.SizeBytes)
			modStr := ""
			if !item.ModTime.IsZero() {
				if item.ModTime.YearDay() == now.YearDay() && item.ModTime.Year() == now.Year() {
					modStr = item.ModTime.Format("15:04")
				} else {
					modStr = item.ModTime.Format("Jan 02")
				}
			}
			metaWidth := len(sizeStr)
			if modStr != "" {
				metaWidth += len(modStr) + 1
			}
			padding := avail - len(display) - metaWidth
			if padding < 0 {
				target := avail - metaWidth
				if target < 3 {
					target = 3
				}
				display = truncateDisplay(name, indicator, maxNameW, target)
				padding = target - len(display)
			}
			if padding > 0 {
				b.WriteString(strings.Repeat(" ", padding))
			}
			if modStr != "" {
				b.WriteString(dimSGR() + sizeStr + " " + modStr + pickerReset())
			} else {
				b.WriteString(dimSGR() + sizeStr + pickerReset())
			}
		}
	}
	return b.String()
}

func (f *FilePickerContext) maxNameWidth() int {
	maxW := 0
	for _, item := range f.items {
		w := len(item.Name) + 1
		if w > maxW {
			maxW = w
		}
	}
	return maxW
}

func truncateDisplay(name, indicator string, maxNameW, avail int) string {
	suffix := indicator
	if len(name)+len(suffix) <= avail {
		return name + suffix
	}
	space := avail - len(suffix)
	if space < 1 {
		if avail < 1 {
			avail = 1
		}
		if len(name) > avail {
			return name[:avail]
		}
		return name
	}
	if len(name) > space {
		if space >= 2 {
			return name[:space-1] + "~" + suffix
		}
		return name[:space] + suffix
	}
	return name + suffix
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.1f%s", float64(bytes)/float64(div), units[exp])
}

func (f *FilePickerContext) footerLineCount() int {
	if f.inputFocused && (f.inputKind == inputKindSearch || f.inputKind == inputKindNewDir) {
		return 2
	}
	if f.mode == PickerSave {
		return 3
	}
	return 2
}

func (f *FilePickerContext) renderFooter() string {
	var b strings.Builder
	switch {
	case f.inputFocused && f.inputKind == inputKindSearch:
		b.WriteString("> Search: ")
		b.WriteString(grid.SanitizeCellEscapes(f.Buffer()))
		b.WriteByte('\n')
		b.WriteString("  " + dimSGR() + "Enter: search  Esc: cancel" + pickerReset())
	case f.inputFocused && f.inputKind == inputKindNewDir:
		b.WriteString("> New directory: ")
		b.WriteString(grid.SanitizeCellEscapes(f.Buffer()))
		b.WriteByte('\n')
		b.WriteString("  " + dimSGR() + "Enter: create  Esc: cancel" + pickerReset())
	case f.mode == PickerSave:
		filename := grid.SanitizeCellEscapes(f.Buffer())
		if f.inputFocused {
			b.WriteString("> File name: ")
		} else {
			b.WriteString("  File name: ")
		}
		b.WriteString(filename)
		b.WriteByte('\n')
		preview := "  " + dimSGR() + "→ " + f.currentPath
		if filename != "" {
			preview += string(filepath.Separator) + filename
		}
		preview += pickerReset()
		// Truncate preview when too long
		if f.viewW > 0 && len(preview) > f.viewW-2 {
			trimmed := "  " + dimSGR() + "→ .../"
			if filename != "" {
				trimmed += filename
			}
			trimmed += pickerReset()
			preview = trimmed
		}
		b.WriteString(preview)
		b.WriteByte('\n')
		if f.inputFocused {
			b.WriteString("  " + dimSGR() + "Enter: confirm  Esc: browse  Ctrl+h: backspace" + pickerReset())
		} else {
			b.WriteString("  " + dimSGR() + "j/k: navigate  i: edit name  h/bs: up  q: cancel" + pickerReset())
		}
	default:
		b.WriteString(f.renderFooterHints())
		b.WriteByte('\n')
		b.WriteString(f.renderFooterActions())
	}
	return b.String()
}

func (f *FilePickerContext) renderFooterHints() string {
	hints := "  " + dimSGR() + "j/k: navigate  h/bs: up  Enter: select  q: cancel" + pickerReset()
	if f.SearchActive() && !f.inputFocused {
		cur, total, truncated := f.SearchStatus()
		var counter string
		if truncated {
			counter = fmt.Sprintf("%d/200+", cur)
		} else {
			counter = fmt.Sprintf("%d/%d", cur, total)
		}
		// Right-align counter
		if f.viewW > 0 {
			padLen := f.viewW - len(hints) + len(dimSGR()) + len(pickerReset())
			padLen -= len(counter)
			if padLen > 1 {
				hints = "  " + dimSGR() + "j/k: navigate  h/bs: up  Enter: select  q: cancel" +
					strings.Repeat(" ", padLen) + counter + pickerReset()
			}
		}
	}
	return hints
}

func (f *FilePickerContext) renderFooterActions() string {
	sortLabel := "name"
	switch f.sortOrder {
	case 1:
		sortLabel = "size"
	case 2:
		sortLabel = "modified"
	}
	return "  " + dimSGR() + "a: new dir  H: home  s: sort  .: hidden  sort: " + sortLabel + pickerReset()
}
