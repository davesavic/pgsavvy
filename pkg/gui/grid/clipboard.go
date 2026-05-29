package grid

import "errors"

// ErrClipboardTooLarge and ErrClipboardUnavailable are the contract sentinels
// a ClipboardWriter may return from Write. The result_tabs_controller typed-
// toast switch matches them via errors.Is. With the shared atotto-backed
// transport these are no longer produced in production, but the contract
// symbols (and the toast switch keyed off them) remain unchanged.

// ErrClipboardTooLarge signals the clipboard payload exceeded the writer's
// size limit and was not published.
var ErrClipboardTooLarge = errors.New("clipboard: value too large")

// ErrClipboardUnavailable signals no clipboard transport was usable.
var ErrClipboardUnavailable = errors.New("clipboard unavailable")
