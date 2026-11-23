export const ErrorBanner = ({ message }: { message: string | null }) =>
  message ? (
    <div className="error-banner" role="alert">
      {message}
    </div>
  ) : null;

export const Loading = () => <div className="loading">Memuat...</div>;