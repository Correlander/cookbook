import CookbookCollection from "./features/cookbooks/CookbookCollection.tsx";

interface PublicCookbooksProps {
  surface?: "page";
  initialQuery?: string;
}

export default function PublicCookbooks({
  surface = "page",
  initialQuery = "",
}: PublicCookbooksProps) {
  return (
    <CookbookCollection
      mode="public"
      surface={surface}
      initialQuery={initialQuery}
    />
  );
}
