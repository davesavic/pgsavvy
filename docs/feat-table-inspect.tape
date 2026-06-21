# VHS tape for the pgsavvy table-inspect modal feature GIF. -> docs/feat-table-inspect.gif
# Recorded by `task demo:features`. Reaches the consolidated schema-rail with
# <tab>, activates the app schema (<cr>, which loads + reveals the Tables tab),
# moves onto app.posts, and opens the table-inspect modal (i), cycling its
# sections with <tab>: Columns -> Indexes -> Foreign Keys -> Constraints.

Output docs/feat-table-inspect.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch + connect off-camera (Hide) so the GIF opens on the feature.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Show
Sleep 600ms

# Focus the schema-rail, activate the app schema (reveals the Tables tab).
Tab
Sleep 1.2s
Enter
Sleep 1.8s

# Move onto app.posts (row 2, after _fixture_meta) and inspect it.
Type "j"
Sleep 1s
Type "i"
Sleep 2.6s

# Cycle sections: Columns -> Indexes -> Foreign Keys -> Constraints.
Tab
Sleep 2.2s
Tab
Sleep 2.2s
Tab
Sleep 2.4s

# Close the modal.
Type "q"
Sleep 800ms
