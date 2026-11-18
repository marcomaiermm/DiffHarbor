import { get } from "node:http";
import { ping } from "fakeweb";
const ITEMS = "https://items.example.com/v1";
const env = { API_URL: process.env.API_URL };
export function loadItems() { return fetch(`${ITEMS}/items`); }
export function loadEnv() { return fetch(env.API_URL!); }
export function token() { return fetch(process.env.TOKEN_URL!); }
export function viaNode() { get("http://node.example.com/x"); }
export function viaPackage() { ping(new URL("https://pkg.example.com/y")); }
export function local() { return fetch("/relative"); }
export function pass() { return loadItems(); }
export function viaPackageEnv() { ping(process.env.PING_URL!); }
