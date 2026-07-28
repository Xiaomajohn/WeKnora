const { BRAND_API_LABEL, BRAND_NAME, getSettings, saveSettings } = require("../../utils/config");

Page({
  data: {
    baseUrl: "",
    apiKey: "",
    brandApiLabel: BRAND_API_LABEL,
    brandName: BRAND_NAME
  },

  onShow() {
    const settings = getSettings();
    this.setData({
      baseUrl: settings.baseUrl,
      apiKey: settings.apiKey
    });
  },

  onBaseUrlInput(event) {
    this.setData({ baseUrl: event.detail.value });
  },

  onApiKeyInput(event) {
    this.setData({ apiKey: event.detail.value });
  },

  save() {
    saveSettings({
      baseUrl: this.data.baseUrl,
      apiKey: this.data.apiKey
    });
    wx.showToast({ title: "Saved", icon: "success" });
  }
});
