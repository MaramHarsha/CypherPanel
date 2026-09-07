// Setting up push-to-deploy, with the half that was missing.
//
// THE FAILURE THIS EXISTS TO STOP, reported by an operator: "when I push to
// main it does not deploy, I have to click the Deploy button".
//
// The webhook endpoint refuses any delivery whose signature does not verify.
// The secret it verifies against was minted at create time and returned exactly
// once, in the create response - which the create dialog discarded. This card
// then told the operator to add the webhook to GitHub and showed them only the
// URL. So they added a webhook with no secret, GitHub got a 401 on every push,
// and nothing deployed. Nothing could read the secret back and nothing could
// replace it, so there was no way out of it either.
//
// The secret is ROTATED rather than revealed: it is sealed under the master key,
// and a control that unseals a credential to display it is one that eventually
// displays it to the wrong person. Minting a new one costs a paste the operator
// is already making. What that breaks - a webhook already configured with the
// old secret - is said before the button is pressed, not after.
import { useState } from "react";
import { useRotateApplicationWebhookSecret } from "@/api/gen/applications/applications";
import { CopyField } from "@/components/copy-field";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { toastFailed } from "@/lib/toast";

export function PushToDeploy({
  appId,
  webhookUrl,
  branch,
}: {
  appId: string;
  webhookUrl: string;
  branch: string;
}) {
  // Held only until this page is left: the API returns it exactly once.
  const [secret, setSecret] = useState<string | null>(null);
  const rotate = useRotateApplicationWebhookSecret({
    mutation: {
      onSuccess: (res) => setSecret(res.webhook.secret),
      onError: (e: unknown) => toastFailed("Could not mint a webhook secret", e),
    },
  });
  const state = useMutationActionState(rotate);

  return (
    <section className="rounded-lg border border-border bg-surface p-4.5">
      <h2 className="eyebrow">Push to deploy</h2>
      <p className="mt-3 max-w-2xl text-[12.5px] leading-relaxed text-text-dim">
        Add this webhook to the GitHub repository (Settings &rarr; Webhooks, content type JSON) and every push to{" "}
        <span className="font-mono text-[12px] text-text">{branch}</span> deploys automatically. It needs{" "}
        <strong className="font-semibold text-text">both</strong> the URL and a secret &mdash; GitHub signs each
        delivery with the secret, and a delivery that does not verify is refused.
      </p>

      <p className="mt-3 text-[11.5px] font-semibold text-text">Payload URL</p>
      <CopyField value={webhookUrl} className="mt-1.5" />

      <p className="mt-4 text-[11.5px] font-semibold text-text">Secret</p>
      {secret ? (
        <>
          <CopyField value={secret} className="mt-1.5" />
          <p className="mt-1.5 text-[12px] leading-[1.5] text-text-mid">
            This is the only time it is shown. Paste it into the webhook&rsquo;s <span className="mono">Secret</span>{" "}
            field on GitHub. The panel stores it sealed and cannot show it again &mdash; mint another if it is lost.
          </p>
        </>
      ) : (
        <>
          <p className="mt-1.5 max-w-2xl text-[12px] leading-[1.5] text-text-mid">
            The panel never shows a stored secret. Mint one, paste it into GitHub, and pushes start deploying.{" "}
            <strong className="font-semibold text-text-dim">
              If a webhook is already configured for this application, minting a new secret stops it working until
              you paste the new one in.
            </strong>
          </p>
          <ActionButton
            variant="secondary"
            size="sm"
            className="mt-2.5"
            state={state}
            busyLabel="Minting&hellip;"
            successLabel="Minted"
            onClick={() => rotate.mutate({ id: appId })}
          >
            Mint a webhook secret
          </ActionButton>
        </>
      )}
    </section>
  );
}
