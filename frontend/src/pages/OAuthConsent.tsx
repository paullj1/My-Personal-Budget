import { useMutation, useQuery } from '@tanstack/react-query';
import { useEffect } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';

import { hasAuthToken, request } from '../api/client';

type ConsentView = {
  request_id: string;
  client_name: string;
  client_uri?: string;
  redirect_to: string;
  scopes: string[];
};

const SCOPE_LABELS: Record<string, string> = {
  'budgets:read': 'See your budgets, balances and transactions',
  'budgets:write': 'Add transactions and itemize receipts'
};

const OAuthConsent = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const requestId = new URLSearchParams(location.search).get('request_id') ?? '';

  // Consent has to be an authenticated act, and the request id is what the user
  // comes back to after the passkey prompt -- so it travels through the login
  // redirect rather than being dropped.
  useEffect(() => {
    if (!hasAuthToken()) {
      navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`, {
        replace: true
      });
    }
  }, [navigate, location.pathname, location.search]);

  const consentQuery = useQuery({
    queryKey: ['oauth-consent', requestId],
    queryFn: () => request<ConsentView>(`/api/v1/oauth/consent?request_id=${encodeURIComponent(requestId)}`),
    enabled: !!requestId && hasAuthToken(),
    retry: false
  });

  const decide = useMutation({
    mutationFn: (approve: boolean) =>
      request<{ redirect_to: string }>('/api/v1/oauth/consent', {
        method: 'POST',
        body: { request_id: requestId, approve }
      }),
    onSuccess: (data) => {
      // Back to the app that sent us here, carrying the code or the refusal.
      window.location.replace(data.redirect_to);
    }
  });

  if (!requestId) {
    return (
      <section className="card">
        <h1>Authorize</h1>
        <p className="error">This link is missing its authorization request. Start again from the app.</p>
      </section>
    );
  }

  const view = consentQuery.data;

  return (
    <section className="card">
      <header className="card__header">
        <div>
          <p className="eyebrow">Authorize</p>
          <h1>{view ? `Connect ${view.client_name}?` : 'Connect an app?'}</h1>
        </div>
      </header>

      {consentQuery.isLoading && <p>Loading request...</p>}
      {consentQuery.error && (
        <p className="error">{(consentQuery.error as Error).message}</p>
      )}

      {view && (
        <>
          <p className="muted">
            {view.client_name} is asking to use your budgets. It will be sent back to{' '}
            <code>{view.redirect_to}</code>.
          </p>
          <div className="key-list">
            <h2>It will be able to</h2>
            {view.scopes.map((scope) => (
              <div key={scope} className="key-row">
                <p className="key-row__title">{SCOPE_LABELS[scope] ?? scope}</p>
              </div>
            ))}
          </div>
          <p className="muted">
            You can disconnect it at any time from Connections. The connection does not expire unless
            you set an expiry there.
          </p>
          <div className="actions">
            <button type="button" disabled={decide.isPending} onClick={() => decide.mutate(true)}>
              {decide.isPending ? 'Connecting…' : 'Allow'}
            </button>
            <button
              type="button"
              className="ghost"
              disabled={decide.isPending}
              onClick={() => decide.mutate(false)}
            >
              Deny
            </button>
          </div>
          {decide.error && <p className="error">{(decide.error as Error).message}</p>}
        </>
      )}
    </section>
  );
};

export default OAuthConsent;
