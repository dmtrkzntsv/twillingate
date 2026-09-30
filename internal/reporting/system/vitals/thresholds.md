Every figure is the 75th percentile (p75) over the range, which is how
Google rates a page. Values from few samples are noisy. LCP, CLS and INP are
reported when the page is hidden. Collection is opt-in: set `data-vitals` on
the script tag. Google's thresholds (good / poor at p75):

- **LCP** (Largest Contentful Paint): ≤ 2500 ms good, > 4000 ms poor
- **INP** (Interaction to Next Paint): ≤ 200 ms good, > 500 ms poor
- **CLS** (Cumulative Layout Shift): ≤ 0.1 good, > 0.25 poor
- **FCP** (First Contentful Paint): ≤ 1800 ms good, > 3000 ms poor
- **TTFB** (Time to First Byte): ≤ 800 ms good, > 1800 ms poor
