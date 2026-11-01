Empty tiles and charts here mean monitoring is not set up for this project, or
it has not run yet: set it up in Settings → Monitoring.

twillingate fetches the project's site on a schedule and records whether it
answered, how long it took, and why it failed when it did not. Once a day it
also reads the site's TLS certificate and the domain's registration. Uptime is
the share of checks that succeeded over the range, and the response time
averages all of them. The certificate and domain tiles show the newest reading
within the range, in days left, and stay blank when that reading has no date
(an http:// origin has no certificate; some domains do not publish an expiry).
