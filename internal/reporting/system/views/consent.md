Whether the client could keep anything on the device when it sent the view.
`unknown` is every view sent without `$consent`, including all history before
the flag existed; the rate leaves it out. `none` includes views sent before
the visitor answered a consent prompt (e.g. the first page view while a
banner is still up), so read the rate as a trend, not as an acceptance rate.
