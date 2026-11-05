export type AnonymousHomePreset = "library_first" | "discovery_first";

interface AnonymousHomeProps {
  preset: AnonymousHomePreset;
  publicInstance: boolean;
  onSignIn: () => void;
}

export default function AnonymousHome({
  preset,
  publicInstance,
  onSignIn,
}: AnonymousHomeProps) {
  const discoveryFirst = preset === "discovery_first" && publicInstance;

  return (
    <section className={`anonymous-home${discoveryFirst ? " anonymous-home--discovery" : ""}`}>
      <div className="anonymous-home__copy">
        <span className="eyebrow">
          {discoveryFirst ? "Browse this Cookbook" : "Good food, gathered"}
        </span>
        <h1>
          {discoveryFirst
            ? "Find something good to make."
            : "A home for the recipes you love."}
        </h1>
        <p>
          {discoveryFirst
            ? "Explore recipes and cookbooks made available by this instance, then sign in to save your favorites and build your own collection."
            : "Keep family favorites close, organize them into personal cookbooks, and share them with the people around your table."}
        </p>
        <div className="anonymous-home__actions">
          {discoveryFirst && (
            <a className="button button--green" href="/discover">
              Browse recipes
            </a>
          )}
          <button className="button button--green" type="button" onClick={onSignIn}>
            Sign in or create an account
          </button>
        </div>
        {!publicInstance && (
          <p className="anonymous-home__private-note">
            This instance keeps its recipes private. Sign in to open your
            personal kitchen.
          </p>
        )}
      </div>
      <div className="anonymous-home__aside" aria-hidden="true">
        <span>YOUR KITCHEN</span>
        <strong>
          A little more
          <br />
          home in every meal.
        </strong>
      </div>
    </section>
  );
}
