# VHS tape for the pgsavvy foreign-key relationship navigation demo.
# Recorded by `task demo`. -> docs/demo-relationships.gif
#
# Queries app.posts (each row references a user via user_id), opens the FK
# relationship panel (<leader>gr), then follows the foreign key to the
# referenced user row with `gd` and jumps back with <c-o>.

Output docs/demo-relationships.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch and connect.
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s

# Activate the app schema.
Type "1"
Sleep 700ms
Enter
Sleep 1.2s

# Run a query over posts (user_id is a foreign key into app.users).
Type "3"
Sleep 700ms
Type "i"
Type "SELECT id, user_id, title, published FROM app.posts ORDER BY id LIMIT 25;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s

# Focus the results grid and open the relationship (FK) panel.
Type "4"
Sleep 900ms
Space
Type "gr"
Sleep 3s
Escape
Sleep 900ms

# Move the cursor onto the user_id column and follow the FK to the
# referenced user (gd opens the parent row in a new tab).
Type "l"
Sleep 900ms
Type "gd"
Sleep 3s

# Jump back to the posts result.
Ctrl+O
Sleep 2.5s
