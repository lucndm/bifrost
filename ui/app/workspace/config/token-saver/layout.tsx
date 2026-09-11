import { createFileRoute } from "@tanstack/react-router";
import TokenSaverPage from "./page";

export const Route = createFileRoute("/workspace/config/token-saver")({
	component: TokenSaverPage,
});