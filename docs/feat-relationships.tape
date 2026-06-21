# VHS tape for the pgsavvy relationship-explorer feature GIF. -> docs/feat-relationships.gif
# Recorded by `task demo:features`. Queries app.posts (each row references a user
# via user_id), opens the right-docked FK relationship panel (<leader>gr) which
# tracks the focused row's parents/children, follows a foreign key into the
# referenced user row with gd (new result tab), and jumps back with <c-o>.

Output docs/feat-relationships.gif

Set Shell bash
Set FontSize 16
Set Width 1280
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, run the query, and focus the grid off-camera (Hide) so the
# GIF opens directly on the posts result.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, user_id, title, published FROM app.posts ORDER BY id LIMIT 20;"
Sleep 600ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Open the relationship (FK) panel.
Space
Type "gr"
Sleep 2.6s

# Move down rows — the panel tracks each row's parents (outbound) / children.
Type "j"
Sleep 1s
Type "j"
Sleep 1.3s

# Move onto the user_id column and follow the foreign key (gd) into the
# referenced user row, which opens in a new result tab.
Type "l"
Sleep 900ms
Type "gd"
Sleep 2.8s

# Jump back to the posts result.
Ctrl+O
Sleep 2.2s
