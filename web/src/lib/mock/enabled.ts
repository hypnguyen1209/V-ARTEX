// Mock flag: injected at build time; only NEXT_PUBLIC_ variables are readable in the browser.
// Set NEXT_PUBLIC_MOCK=1 on Vercel to run the entire site with mock data and no backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
