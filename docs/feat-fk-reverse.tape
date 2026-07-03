# VHS tape for the pgsavvy reverse-FK picker feature GIF. -> docs/feat-fk-reverse.gif
# Recorded by `task demo:features`. Queries app.users (which is referenced by
# app.posts.user_id and app.user_roles.user_id), focuses the grid, and opens
# the reverse-FK picker (gD) to show every table with an inbound FK pointing
# to app.users. Cycles referencing tables with Tab, selects one (Enter), and
# opens the child rows in a new result tab.
# Use only test fixture data — never record real databases.

Output docs/feat-fk-reverse.gif

Set Shell bash
Set FontSize 16
Set Width 1280
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, run query, and focus the grid off-camera (Hide) so the GIF
# opens directly on the users result.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name FROM app.users ORDER BY id LIMIT 5;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Open the reverse-FK picker (gD). The picker shows every table with an
# inbound FK referencing app.users (app.user_roles, app.posts) tabbed per FK.
Type "gD"
Sleep 2.5s

# Cycle the referencing-table tabs with Tab.
Tab
Sleep 1.8s
Tab
Sleep 1.8s

# Select the first reference (app.user_roles → joins user_id to users).
Enter
Sleep 2.8s

# The new result tab shows parameterised child rows. Cycle back (Ctrl+O)
# to return to the original users result.
Ctrl+O
Sleep 2s
