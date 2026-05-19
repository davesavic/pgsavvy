package editor

// Buffer is the canonical text + cursor + undo state for one QUERY_EDITOR
// window. wwd.1 ships this empty shell so QueryEditorContext (and any
// other early consumer in the editor epic) can hold a *Buffer pointer
// while later child tasks fill in the real fields and methods:
//
//   - wwd.2 adds Lines, Cursor, Marks, Jumps, Selection, History,
//     ConnectionID/Path/UUID, Dirty, and the sync.RWMutex; the diff-based
//     Apply(Edit) entry point; and the LinesCopy / TextInRange / etc.
//     accessors.
//   - wwd.3 layers in cursor / marks / jump-list helpers.
//   - wwd.9 adds the persistence fields and SaveBuffer worker dispatch.
//
// Keeping the body empty in wwd.1 avoids speculative fields that the
// later tasks would have to delete or rename.
type Buffer struct{}
