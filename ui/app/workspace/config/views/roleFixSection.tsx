import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useCreatePluginMutation, useGetPluginsQuery, useUpdatePluginMutation } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const ROLEFIX_PLUGIN_NAME = "rolefix";

// parseModels turns the textarea into a clean glob list: one per line, commas
// also accepted, blanks and duplicates dropped.
function parseModels(text: string): string[] {
	const seen = new Set<string>();
	for (const raw of text.split(/[\n,]/)) {
		const glob = raw.trim();
		if (glob) seen.add(glob);
	}
	return [...seen];
}

// RoleFixSection configures the built-in rolefix plugin, which rewrites
// system/developer roles for models whose chat template only accepts a single
// leading system message (e.g. Qwen3.x on SGLang).
export default function RoleFixSection() {
	const hasCreateAccess = useRbac(RbacResource.Plugins, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Plugins, RbacOperation.Update);
	const { data: plugins, isLoading: isLoadingPlugins } = useGetPluginsQuery();
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();

	const plugin = useMemo(() => plugins?.find((p) => p.name === ROLEFIX_PLUGIN_NAME), [plugins]);
	const savedEnabled = plugin?.enabled ?? false;
	const savedModels: string[] = useMemo(
		() => (Array.isArray(plugin?.config?.models) ? plugin.config.models.filter((m: unknown) => typeof m === "string") : []),
		[plugin],
	);

	const [enabled, setEnabled] = useState(false);
	const [modelsText, setModelsText] = useState("");

	useEffect(() => {
		setEnabled(savedEnabled);
		setModelsText(savedModels.join("\n"));
	}, [savedEnabled, savedModels]);

	const models = useMemo(() => parseModels(modelsText), [modelsText]);
	const hasChanges = enabled !== savedEnabled || models.join("\n") !== savedModels.join("\n");
	const canEdit = plugin ? hasUpdateAccess : hasCreateAccess;
	const isSaving = isCreating || isUpdating;

	const handleSave = useCallback(async () => {
		const config = { ...(plugin?.config ?? {}), models };
		try {
			if (plugin) {
				await updatePlugin({
					name: ROLEFIX_PLUGIN_NAME,
					data: { enabled, config },
				}).unwrap();
			} else {
				await createPlugin({
					name: ROLEFIX_PLUGIN_NAME,
					path: "",
					enabled,
					config,
				}).unwrap();
			}
			toast.success("Role rewrite settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [plugin, models, enabled, updatePlugin, createPlugin]);

	const status = plugin?.status?.status;

	return (
		<div className="space-y-4 border-t pt-6" data-testid="rolefix-section">
			<div className="space-y-0.5">
				<h2 className="text-base font-semibold">Role Rewrite</h2>
				<p className="text-muted-foreground text-sm">
					For matching models, fold leading system/developer messages into the instructions (Responses) or a single leading system message
					(Chat), and turn later ones into user messages prefixed with <code>[system]</code>. Needed for chat templates such as Qwen3.x that
					reject the developer role or a system message that is not first. Applies to every request whose model matches; other models are
					left untouched.
				</p>
			</div>

			<div className="flex items-center justify-between space-x-2">
				<div className="space-y-0.5">
					<label htmlFor="rolefix-enabled" className="text-sm font-medium">
						Enable Role Rewrite
					</label>
					<p className="text-muted-foreground text-sm">
						{plugin ? (
							<>
								Plugin status: <span data-testid="rolefix-status">{status ?? "unknown"}</span>
							</>
						) : (
							"Not configured yet. Saving creates the plugin."
						)}
					</p>
				</div>
				<Switch
					id="rolefix-enabled"
					data-testid="rolefix-enabled"
					size="md"
					checked={enabled}
					onCheckedChange={setEnabled}
					disabled={!canEdit || isLoadingPlugins}
				/>
			</div>

			<div className="space-y-2">
				<label htmlFor="rolefix-models" className="text-sm font-medium">
					Model Globs
				</label>
				<p className="text-muted-foreground text-sm">
					One per line, matched case-insensitively against <code>provider/model</code> and the bare model name. Wildcards work anywhere:{" "}
					<code>*</code> matches any characters (including <code>/</code>), <code>?</code> matches one character. Examples:{" "}
					<code>redpill/qwen/*</code>, <code>*qwen*</code>, <code>redpill/*/qwen3.?-27b</code>.
				</p>
				<Textarea
					id="rolefix-models"
					data-testid="rolefix-models"
					className="min-h-[120px] font-mono text-sm"
					placeholder={"redpill/qwen/*\n*qwen*"}
					value={modelsText}
					onChange={(e) => setModelsText(e.target.value)}
					disabled={!canEdit || isLoadingPlugins}
				/>
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isSaving || !canEdit} data-testid="rolefix-save-button">
					{isSaving ? "Saving..." : "Save Role Rewrite"}
				</Button>
			</div>
		</div>
	);
}
