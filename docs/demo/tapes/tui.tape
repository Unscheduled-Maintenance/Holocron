# The interactive archive: browse, read an entry, search, report.
Output docs/images/tui.gif

Type "holocron"
Sleep 300ms
Enter
Sleep 2s

# The detail pane follows the selection; entry text is rendered Markdown.
Down@500ms 2
Sleep 1.5s
Screenshot docs/images/tui.png
Sleep 500ms
Down@450ms 3
Sleep 1s

# Search as you type, with matches highlighted.
Type "/"
Sleep 400ms
Type@120ms "exporter"
Sleep 1.5s
Enter
Sleep 1s
Escape
Sleep 800ms

# Generate the staff update, then show which entries each item came from.
Type "R"
Sleep 700ms
Type@120ms "staff"
Sleep 400ms
Enter
Sleep 2.5s
Type "i"
Sleep 2s
Escape
Sleep 600ms
Type "q"
Sleep 500ms
