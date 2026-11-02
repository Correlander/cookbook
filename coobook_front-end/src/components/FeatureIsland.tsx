import { Component, Suspense } from "react";
import type { ReactNode } from "react";

interface FeatureBoundaryProps {
  children: ReactNode;
  name: string;
}

interface FeatureBoundaryState {
  failed: boolean;
}

class FeatureBoundary extends Component<
  FeatureBoundaryProps,
  FeatureBoundaryState
> {
  state: FeatureBoundaryState = { failed: false };

  static getDerivedStateFromError(): FeatureBoundaryState {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      return (
        <p className="form-message" role="alert">
          {this.props.name} could not be loaded. Reload the page to try again.
        </p>
      );
    }

    return this.props.children;
  }
}

export default function FeatureIsland({
  name,
  loading,
  children,
}: {
  name: string;
  loading: string;
  children: ReactNode;
}) {
  return (
    <FeatureBoundary name={name}>
      <Suspense
        fallback={
          <p className="empty-state" role="status">
            Loading {loading}…
          </p>
        }
      >
        {children}
      </Suspense>
    </FeatureBoundary>
  );
}
