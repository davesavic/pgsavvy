# VHS tape for the pgsavvy saved-queries feature GIF. -> docs/feat-saved-queries.gif
# Recorded by `task demo:features`. Types a query in the editor, saves it with
# a name (<leader>s — prompts for name, persists to queries.yml), then opens
# the saved-queries tab (<leader>o) to browse, navigate, and confirm the
# saved query is listed. Cancels without re-inserting so re-recording is
# idempotent.
# Use only test fixture data — never record real databases.

Output docs/feat-saved-queries.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch, connect, and type a query off-camera (Hide) so the GIF opens with
# the statement ready to be saved.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT count(*) AS total FROM app.posts WHERE published;"
Escape
Sleep 500ms
Show
Sleep 800ms

# Save the query (<leader>s). A prompt appears asking for a name.
Space
Type "s"
Sleep 800ms
Type "Published post count"
Sleep 600ms
Enter
Sleep 2s

# Open the saved-queries tab (<leader>o) to browse the saved list.
Space
Type "o"
Sleep 2.2s

# Navigate the saved-query list. The saved query appears with its name.
Type "j"
Sleep 800ms
Type "k"
Sleep 1.2s

# Return to the query editor.
Type "3"
Sleep 1s
