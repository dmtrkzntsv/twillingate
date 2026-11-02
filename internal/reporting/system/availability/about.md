Empty cards here mean monitoring is not set up for this project, or it has not
run yet: set it up in Settings → Monitoring.

twillingate fetches each monitored site on a schedule and
records whether it answered, how long it took, and why it failed when it did
not. Once a day it also reads each site's TLS certificate and the registration
of each main domain, which subdomains share. Uptime is the share of checks that
succeeded over the range, and the response time averages all of them, across
every site and for each in Sites. Certificates and Domains show the newest
reading within the range, in days left, and stay blank when that reading has no
date (an http:// origin has no certificate; some domains do not publish an
expiry). Click a column's header to sort, such as by the days left.
