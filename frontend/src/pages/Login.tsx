import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';

import { persistToken, request } from '../api/client';
import { LoginResponse } from '../types';
import { bufferFromBase64Url, toBase64Url } from '../utils/bytes';

const Login = () => {
  const navigate = useNavigate();
  const [pkEmail, setPkEmail] = useState('');

  const passkeyLogin = useMutation({
    mutationFn: async () => {
      const begin = await request<{
        challenge: string;
        rpId: string;
        allowCredentials: { id: string; type: string }[];
        timeout: number;
      }>('/api/v1/auth/passkeys/login/begin', {
        method: 'POST',
        body: { email: pkEmail }
      });

      const publicKeyOpts = (begin as any).publicKey ?? begin;
      if (!publicKeyOpts?.challenge) {
        throw new Error('Login options missing challenge.');
      }

      const publicKey: PublicKeyCredentialRequestOptions = {
        challenge: bufferFromBase64Url(publicKeyOpts.challenge),
        rpId: publicKeyOpts.rpId,
        allowCredentials: (publicKeyOpts.allowCredentials || []).map((cred: any) => ({
          ...cred,
          id: bufferFromBase64Url(cred.id)
        })),
        userVerification: 'preferred',
        timeout: publicKeyOpts.timeout
      };

      const assertion = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
      if (!assertion) {
        throw new Error('Passkey login cancelled');
      }

      const assertionResponse = assertion.response as AuthenticatorAssertionResponse;
      const finish = await request<LoginResponse>('/api/v1/auth/passkeys/login/finish', {
        method: 'POST',
        body: {
          email: pkEmail,
          id: assertion.id,
          rawId: toBase64Url(assertion.rawId),
          type: assertion.type,
          response: {
            clientDataJSON: toBase64Url(assertionResponse.clientDataJSON),
            authenticatorData: toBase64Url(assertionResponse.authenticatorData),
            signature: toBase64Url(assertionResponse.signature),
            userHandle: assertionResponse.userHandle ? toBase64Url(assertionResponse.userHandle) : null
          }
        }
      });
      persistToken(finish.token);
      navigate('/dashboard');
    }
  });

  return (
    <section className="card">
      <header className="card__header">
        <div>
          <p className="eyebrow">Access</p>
          <h1>Login</h1>
        </div>
      </header>
      <p className="muted">Use your passkey to get a JWT and access the app.</p>

      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          passkeyLogin.mutate();
        }}
      >
        <label>
          Email
          <input
            type="email"
            placeholder="you@example.com"
            autoComplete="email"
            required
            value={pkEmail}
            onChange={(e) => setPkEmail(e.target.value)}
          />
        </label>
        <button type="submit" disabled={passkeyLogin.isPending}>
          {passkeyLogin.isPending ? 'Waiting for passkey…' : 'Use passkey'}
        </button>
        {passkeyLogin.error && <p className="error">{(passkeyLogin.error as Error).message}</p>}
      </form>
    </section>
  );
};

export default Login;
