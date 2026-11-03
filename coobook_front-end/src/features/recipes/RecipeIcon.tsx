type IconName =
  "search" | "book" | "leaf" | "arrow" | "clock" | "close" | "chef";

export default function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  const d = {
    search: (
      <>
        <circle cx="11" cy="11" r="7" />
        <path d="m20 20-4-4" />
      </>
    ),
    book: (
      <>
        <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v17H6.5A2.5 2.5 0 0 1 4 17.5z" />
        <path d="M4 6h13M8 8h5" />
      </>
    ),
    leaf: (
      <>
        <path d="M20 4c-8 0-14 3-14 10a6 6 0 0 0 6 6c7 0 8-8 8-16Z" />
        <path d="M4 21c3-5 7-8 12-11" />
      </>
    ),
    arrow: (
      <>
        <path d="M5 12h14M13 6l6 6-6 6" />
      </>
    ),
    clock: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7v5l3 2" />
      </>
    ),
    close: <path d="m6 6 12 12M18 6 6 18" />,
    chef: (
      <>
        <path d="M6 13v7h12v-7M4 9a4 4 0 0 1 4-4 5 5 0 0 1 8 0 4 4 0 0 1 4 4c0 2-1 4-4 4H8c-3 0-4-2-4-4Z" />
        <path d="M10 20v-4h4v4" />
      </>
    ),
  };
  return (
    <svg
      aria-hidden="true"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {d[name]}
    </svg>
  );
}
