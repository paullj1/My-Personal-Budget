import { FormEvent, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';

import { persistToken, request } from '../api/client';
import { LoginResponse } from '../types';
import { bufferFromBase64Url, toBase64Url } from '../utils/bytes';

const Register = () => {
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');

  const begin = useMutation({
    mutationFn: () =>
      request<Record<string, any>>('/api/v1/auth/passkeys/begin', {
        method: 'POST',
        body: { email }
      })
  });

  const finish = useMutation<LoginResponse, Error, Record<string, any>>({
    mutationFn: (payload) =>
      request<LoginResponse>('/api/v1/auth/passkeys/finish', {
        method: 'POST',
        body: payload
      })
  });

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    begin.reset();
    finish.reset();
    try {
      const options = await begin.mutateAsync();
      const publicKeyOpts = (options as any).publicKey ?? options;
      if (!publicKeyOpts?.challenge || !publicKeyOpts?.user?.id) {
        throw new Error('Registration options missing challenge or user id.');
      }

      const publicKeyOptions: PublicKeyCredentialCreationOptions = {
        ...publicKeyOpts,
        challenge: bufferFromBase64Url(publicKeyOpts.challenge),
        user: {
          ...publicKeyOpts.user,
          id: bufferFromBase64Url(publicKeyOpts.user.id)
        }
      };
      if (Array.isArray(publicKeyOpts.excludeCredentials)) {
        publicKeyOptions.excludeCredentials = publicKeyOpts.excludeCredentials.map((cred: any) => ({
          ...cred,
          id: bufferFromBase64Url(cred.id)
        }));
      }

      const credential = (await navigator.credentials.create({ publicKey: publicKeyOptions })) as PublicKeyCredential | null;
      if (!credential) {
        throw new Error('Passkey creation cancelled');
      }
      const attestation = credential.response as AuthenticatorAttestationResponse;
      const finishPayload = {
        email,
        id: credential.id,
        rawId: toBase64Url(credential.rawId),
        type: credential.type,
        response: {
          clientDataJSON: toBase64Url(attestation.clientDataJSON),
          attestationObject: toBase64Url(attestation.attestationObject)
        }
      };

      const auth = await finish.mutateAsync(finishPayload);
      if (!auth.token) {
        throw new Error('Registration did not return a token.');
      }
      persistToken(auth.token);
      navigate('/dashboard');
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <section className="card">
      <header className="card__header">
        <div>
          <p className="eyebrow">Access</p>
          <h1>Register Passkey</h1>
        </div>
      </header>
      <p className="muted">
        Creates a passkey via WebAuthn and records the challenge server-side (attestation not verified).
      </p>
      <form className="form" onSubmit={onSubmit}>
        <label>
          Email
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoComplete="email"
          />
        </label>
        <button type="submit" disabled={begin.isPending || finish.isPending}>
          {begin.isPending || finish.isPending ? 'Registering…' : 'Register passkey'}
        </button>
      </form>
      {error && <p className="error">{error}</p>}
    </section>
  );
};

export default Register;
