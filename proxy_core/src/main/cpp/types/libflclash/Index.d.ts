
export const initClash: (path: string, version: string) => void;
export const startTun: (fd: number, callback: (id: number, fd: number) => void) => string;
export const getVpnOptions: () => string;
export const setFdMap: (fd: number) => void;
// Empty means listener/TUN cleanup reported no error; nonempty is unresolved.
// This result does not confirm that the system VPN has been destroyed.
export const stopTun: () => string;
export const getTunStartToken: () => string;
export const beginPlatformNetwork: (owner: string) => Promise<string>;
export const completePlatformNetwork: (owner: string) => string;
export const forceGc: () => void;
export const validateConfig: (paramsString: string) => Promise<string>;
export const updateConfig: (paramsString: string) => Promise<string>;
export const getCountryCode: (ip: string) => Promise<string>
export const getProxies: () => string;
export const changeProxy: (params: string) => Promise<string>;
export const getTraffic: (onlyProxy?: boolean) => string;
export const getTotalTraffic: (onlyProxy?: boolean) => string;
export const publishNetworkSnapshot: (snapshot: string) => string;
export const resetTraffic: () => void;
export const asyncTestDelay: (paramsString: string) => Promise<string>;
export const getExternalProviders: () => string;
export const getExternalProvider: (name: string) => string;
export const updateExternalProvider: (name: string) => Promise<string>;
export const sideLoadExternalProvider: (name: string, data: Uint8Array) => Promise<string>;
export const updateGeoData: (type: string, name: string) => Promise<string>;
export const getConnections: () => Promise<string>;
export const closeConnections: () => string;
export const closeConnection: (connectionId: string) => string;
export const getRequestList: () => string;
export const clearRequestList: () => string;
export const registerMessage: (callback: (message: string, value: string) => void) => string | void;
export const unregisterMessage: () => void;
export const startLog: (callback: (message: string, value: string) => void) => string;
// True means the system TUN is ready and all configured proxy listeners bound.
export const startListener: () => boolean
export const stopListener: () => void
export const stopLog: () => void;
export const startIpc: (path) => void;
