# VHS tape for the pgsavvy EXPLAIN plan-tree feature GIF. -> docs/feat-explain-plan.gif
# Recorded by `task demo:features`. Runs EXPLAIN ANALYZE on a join/aggregate,
# focuses the interactive plan tree, toggles the plan-doctor insights strip (i),
# walks nodes (j/k, Enter to collapse), and jumps to the heaviest node (H).

Output docs/feat-explain-plan.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, and type the query off-camera (Hide) so the GIF opens on the
# query ready to be explained.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT u.name, count(p.id) AS posts FROM app.users u JOIN app.posts p ON p.user_id = u.id GROUP BY u.name ORDER BY posts DESC LIMIT 10;"
Sleep 500ms
Escape
Sleep 400ms
Show
Sleep 800ms

# EXPLAIN ANALYZE (<leader>E) opens the plan in a result tab.
Space
Type "E"
Sleep 3.5s

# Focus the plan tab and explore it.
Type "4"
Sleep 1s

# Toggle the plan-doctor insights strip.
Type "i"
Sleep 2.8s

# Walk the tree and collapse a node.
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Enter
Sleep 1.5s
Enter
Sleep 1.2s

# Jump to the heaviest descendant node.
Type "H"
Sleep 2.5s
